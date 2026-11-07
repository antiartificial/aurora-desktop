//! Winit backend: a real window on the host, for seeing the desktop.
//!
//! Two EGL contexts, because of one driver bug.
//!
//! Binding an offscreen texture makes Smithay call `eglMakeCurrent` with no
//! surface (`GlesRenderer::bind` for a texture, `wait`, `map_texture`,
//! `with_context`). On this host -- NVIDIA 580, KWin, PLATFORM_WAYLAND_KHR --
//! re-binding the window surface afterwards hands out a back buffer that
//! `eglSwapBuffers` does not present. Every call returns success and the
//! window stays blank. See `examples/winit_bisect.rs` (phases A-D) and
//! `examples/winit_red.rs`.
//!
//! So the window's context (A, owned by the winit backend) never goes
//! surfaceless: per frame it binds the window, draws ONE texture over it and
//! swaps. All other work happens in context B, a second `EGLContext` shared
//! with A, driving its own `GlesRenderer`: the scene, the effects pipeline,
//! capture read-back and the trusted overlay, exactly as headless does
//! (`render::render_and_capture`), into a texture B owns. Shared contexts
//! share texture objects, so A samples B's texture.
//!
//! Client buffers (shm/dmabuf imports) are imported by B only: the scene is
//! built from B's renderer, and Smithay caches imports per renderer context
//! id. A never sees a client surface.
//!
//! Ordering: B's work is finished (`glFinish`) before A binds the window, so
//! the window is never left unbound at swap time and A reads a complete
//! texture.
//!
//! Orientation: the output transform is `Normal`, like headless, so capture
//! output is identical to headless. The texture holds the scene in Smithay's
//! texture orientation; A draws it with a `Flipped180` frame transform, which
//! is what a direct render into a window needs (GL's origin is bottom-left,
//! the window's top-left) -- the same transform the direct path used.
//!
//! If B cannot be created, this falls back to rendering straight into the
//! window (no effects, no capture), the previous behaviour.

use smithay::{
    backend::{
        SwapBuffersError,
        allocator::Fourcc,
        egl::EGLContext,
        renderer::{
            Bind, Frame, Offscreen, Renderer,
            damage::OutputDamageTracker,
            gles::{GlesRenderer, GlesTarget, GlesTexture},
        },
        winit::{self, WinitEvent},
    },
    output::{Mode, Output, PhysicalProperties, Subpixel},
    reexports::calloop::EventLoop,
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform},
};

use crate::{Aurora, render};

/// Context B's half of the frame: its renderer and the texture it draws into.
struct Offscreen2 {
    renderer: GlesRenderer,
    target: GlesTexture,
    size: Size<i32, Physical>,
}

/// A second renderer on an EGL context shared with the window's.
fn make_offscreen(window_renderer: &GlesRenderer, size: Size<i32, Physical>) -> Result<Offscreen2, Box<dyn std::error::Error>> {
    let a = window_renderer.egl_context();
    let ctx = EGLContext::new_shared(a.display(), a)?;
    let mut renderer = unsafe { GlesRenderer::new(ctx)? };
    let target = new_target(&mut renderer, size)?;
    Ok(Offscreen2 { renderer, target, size })
}

fn new_target(r: &mut GlesRenderer, size: Size<i32, Physical>) -> Result<GlesTexture, Box<dyn std::error::Error>> {
    Ok(r.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((size.w, size.h)))?)
}

/// Draw `texture` (made by another context) over the whole window. A's only job.
pub fn present_texture(
    renderer: &mut GlesRenderer,
    window: &mut GlesTarget<'_>,
    texture: &GlesTexture,
    size: Size<i32, Physical>,
) -> Result<(), Box<dyn std::error::Error>> {
    let full = Rectangle::<i32, Physical>::from_size(size);
    let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
    let mut f = renderer.render(window, size, Transform::Flipped180)?;
    f.render_texture_from_to(texture, src, full, &[full], &[], Transform::Normal, 1.0, None, &[])?;
    let _ = f.finish()?;
    Ok(())
}

/// The host window's Wayland `app_id` and the basename of its `.desktop` file.
pub const APP_ID: &str = "org.aurora.Desktop";

pub fn init_winit(
    event_loop: &mut EventLoop<Aurora>,
    state: &mut Aurora,
) -> Result<(), Box<dyn std::error::Error>> {
    // Same size and visibility as Smithay's `winit::init`, plus the two things a launcher keys on:
    // the title and the Wayland `app_id` (`WindowAttributesWayland::with_name`; its second argument
    // is a no-op). Plasma resolves `APP_ID` against `org.aurora.Desktop.desktop`, whose `Icon=` is the
    // one installed by `make icon` from branding/. Keep the three in step.
    use smithay::reexports::winit::{
        dpi::LogicalSize, platform::wayland::WindowAttributesWayland, window::WindowAttributes,
    };
    let attributes = WindowAttributes::default()
        .with_surface_size(LogicalSize::new(1280.0, 800.0))
        .with_title("Aurora")
        .with_visible(true)
        .with_platform_attributes(Box::new(
            WindowAttributesWayland::default().with_name(APP_ID, APP_ID),
        ));
    let (mut backend, winit) = winit::init_from_attributes::<GlesRenderer>(attributes)?;
    let win_size = backend.window_size();
    // winit's wl_display, for the drag-and-drop bridge (src/dnd). Absent on an X11 host.
    {
        use smithay::reexports::winit::raw_window_handle::{HasDisplayHandle, RawDisplayHandle};
        if let Ok(h) = backend.window().display_handle() {
            if let RawDisplayHandle::Wayland(w) = h.as_raw() {
                state.host_display = Some(crate::dnd::ForeignDisplay(w.display.as_ptr()));
            }
        }
        // The window's own surface. A drag handed OUT to the host has to name it as the
        // origin, because `start_drag` is granted only to the surface holding the implicit
        // pointer grab, and on a winit host that is this window. Without it a guest drag can
        // still happen, it just cannot leave Aurora, and the startup line says so.
        // Decision 0020, "Guest to host".
        state.host_surface = crate::dnd::out::surface_of(backend.window());
    }
    // TEST AFFORDANCE: AURORA_TEST_NO_SHARED_EGL=1 behaves as if context B could
    // not be created, so the direct fallback below can be exercised. Loud, and
    // only ever makes the compositor LESS capable (no effects, no capture).
    let forced_fallback = std::env::var_os("AURORA_TEST_NO_SHARED_EGL").is_some_and(|v| !v.is_empty());
    if forced_fallback {
        eprintln!("aurora-compositor: WARNING: AURORA_TEST_NO_SHARED_EGL is set: the second EGL context is NOT created; no effects, no capture (test only)");
    }
    let made = if forced_fallback { Err("forced by AURORA_TEST_NO_SHARED_EGL".into()) } else { make_offscreen(backend.renderer(), win_size) };
    tracing::info!("winit: window {}x{} physical, host scale factor {}; the output carries that scale, clients get it logically", win_size.w, win_size.h, backend.scale_factor());
    let mut off = match made {
        Ok(o) => Some(o),
        Err(e) => {
            tracing::warn!("winit: no second EGL context ({e}); drawing straight into the window, no effects or capture");
            None
        }
    };

    let mode = Mode { size: backend.window_size(), refresh: 60_000 };
    let output = Output::new(
        "winit".to_string(),
        PhysicalProperties {
            size: (0, 0).into(),
            subpixel: Subpixel::Unknown,
            make: "Smithay".into(),
            model: "Winit".into(),
            serial_number: "Unknown".into(),
        },
    );
    let _global = output.create_global::<Aurora>(&state.display_handle);
    // With the offscreen pass the output is Normal, as in headless; the window
    // flip happens in `present_texture`. The direct fallback draws into the
    // window itself and needs Smithay's winit convention, Flipped180.
    let transform = if off.is_some() { Transform::Normal } else { Transform::Flipped180 };
    // The output carries the host's scale: the space is logical, the scene is
    // rendered and captured in the window's physical pixels (src/scale.rs).
    output.change_current_state(Some(mode), Some(transform), None, Some((0, 0).into()));
    output.set_preferred(mode);
    state.set_host_scale(&output, backend.scale_factor());
    state.space.map_output(&output, (0, 0));

    backend.window().request_redraw();
    let mut damage_tracker = OutputDamageTracker::from_output(&output);
    // What the host has been told the pointer looks like. Kept so set_cursor is sent on a
    // CHANGE and not on every pointer sample.
    let mut shown_cursor = crate::cursor::Shape::Default;

    event_loop.handle().insert_source(winit, move |event, _, state| {
        match event {
            WinitEvent::Resized { size, scale_factor } => {
                tracing::debug!("winit: resized to {}x{} physical (host scale {scale_factor})", size.w, size.h);
                output.change_current_state(Some(Mode { size, refresh: 60_000 }), None, None, None);
                state.set_host_scale(&output, scale_factor);
                state.update_capture_constraints(&output);
            }
            WinitEvent::Input(event) => {
                state.process_input_event(event);
                // Apply the pointer shape the motion just decided. Only when it CHANGES: winit
                // forwards every set_cursor to the host compositor, and doing that per motion
                // event would be a request per pointer sample.
                if state.cursor != shown_cursor {
                    shown_cursor = state.cursor;
                    backend.window().set_cursor(shown_cursor.icon().into());
                }
            }
            WinitEvent::Redraw => {
                crate::dnd::tick(state);
                let size = backend.window_size();
                if size.w <= 0 || size.h <= 0 {
                    backend.window().request_redraw();
                    return;
                }
                // `window_size()` reads the live window, which can be ahead of
                // the last Resized event: keep the output mode (and the capture
                // constraints derived from it) equal to the size drawn now.
                if output.current_mode().map(|m| m.size) != Some(size) {
                    output.change_current_state(Some(Mode { size, refresh: 60_000 }), None, None, None);
                    state.update_capture_constraints(&output);
                }
                let full = Rectangle::<i32, Physical>::from_size(size);
                let drew = if let Some(o) = off.as_mut() {
                    // 1. All of B's work, finished.
                    if o.size != size {
                        match new_target(&mut o.renderer, size) {
                            Ok(t) => { o.target = t; o.size = size; }
                            Err(e) => tracing::error!("winit: cannot resize the offscreen target: {e}"),
                        }
                    }
                    if o.size == size {
                        match o.renderer.bind(&mut o.target) {
                            Ok(mut fb) => render::render_and_capture(state, &mut o.renderer, &mut fb, &output, &mut damage_tracker, size),
                            Err(e) => tracing::error!("winit: cannot bind the offscreen target: {e}"),
                        }
                        // Make the shared texture complete before A samples it.
                        let _ = o.renderer.with_context(|gl| unsafe { gl.Finish() });
                    }
                    // 2. Only now A: bind the window, one texture, swap. If the
                    // target could not be resized, skip the frame rather than
                    // sample a texture of the wrong size.
                    if o.size != size {
                        false
                    } else { match backend.bind() {
                        Ok((renderer, mut window)) => {
                            if let Err(e) = present_texture(renderer, &mut window, &o.target, size) {
                                tracing::error!("winit: drawing the frame into the window failed: {e}");
                            }
                            true
                        }
                        Err(e) => {
                            tracing::error!("winit: cannot bind the window, stopping: {e}");
                            state.loop_signal.stop();
                            false
                        }
                    } }
                } else {
                    // One bind, one render, one swap, and nothing in between that
                    // could make the context current without the surface.
                    match backend.bind() {
                        Ok((renderer, mut window)) => {
                            render::render_direct(state, renderer, &mut window, &output, &mut damage_tracker, size);
                            true
                        }
                        Err(e) => {
                            tracing::error!("winit: cannot bind the window, stopping: {e}");
                            state.loop_signal.stop();
                            false
                        }
                    }
                };
                if drew {
                    if let Err(e) = backend.submit(Some(&[full])) {
                        let why = match e {
                            SwapBuffersError::ContextLost(e) => format!("context lost: {e}"),
                            e => format!("swap: {e}"),
                        };
                        tracing::error!("winit: presenting failed, stopping: {why}");
                        state.loop_signal.stop();
                        return;
                    }
                }
                render::after_frame(state, &output);
                backend.window().request_redraw();
            }
            WinitEvent::CloseRequested => state.loop_signal.stop(),
            _ => (),
        };
    })?;

    Ok(())
}
