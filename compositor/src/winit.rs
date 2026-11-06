//! Winit backend. The whole frame (effects, capture read-back, trusted overlay)
//! is rendered into an offscreen texture exactly as `headless` does; the window
//! surface is only touched at the very end, to blit that texture and swap.
//!
//! Why not render into the window surface directly: several Smithay calls
//! (`GlesRenderer::wait`, `map_texture`, binding a texture) make the EGL context
//! current with *no* surface. After that `eglSwapBuffers` fails with
//! BAD_SURFACE ("not current draw surface"), and even re-binding the surface
//! gives a fresh back buffer on the NVIDIA driver, so the drawn frame is lost
//! (a capture read back black). Keeping the surface out of everything but the
//! final blit avoids both.

use smithay::{
    backend::{
        SwapBuffersError,
        allocator::Fourcc,
        renderer::{
            Bind, Frame, Offscreen, Renderer, damage::OutputDamageTracker,
            gles::{GlesRenderer, GlesTexture},
        },
        winit::{self, WinitEvent, WinitGraphicsBackend},
    },
    output::{Mode, Output, PhysicalProperties, Subpixel},
    reexports::calloop::EventLoop,
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform},
};

use crate::{Aurora, render};

/// Transform used when blitting the offscreen frame onto the window surface.
const BLIT: Transform = Transform::Flipped180;

fn make_target(r: &mut GlesRenderer, size: Size<i32, Physical>) -> Result<GlesTexture, Box<dyn std::error::Error>> {
    Ok(r.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((size.w.max(1), size.h.max(1))))?)
}

/// Blit `frame` to the window and swap. The first (empty) frame on the window
/// is a precaution: it lets Smithay run deferred cleanup (which can unbind the
/// surface) before the frame that is swapped;
/// the second frame then draws and swaps with nothing in between.
fn present(backend: &mut WinitGraphicsBackend<GlesRenderer>, frame: &GlesTexture, size: Size<i32, Physical>) -> Result<(), String> {
    let full = Rectangle::<i32, Physical>::from_size(size);
    for pass in 0..2 {
        let (renderer, mut window) = backend.bind().map_err(|e| format!("bind: {e}"))?;
        let mut f = renderer.render(&mut window, size, BLIT).map_err(|e| format!("render: {e}"))?;
        if pass == 1 {
            let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
            f.render_texture_from_to(frame, src, full, &[full], &[], Transform::Normal, 1.0, None, &[])
                .map_err(|e| format!("blit: {e}"))?;
        }
        let _sync = f.finish().map_err(|e| format!("finish: {e}"))?;
    }
    backend.submit(Some(&[full])).map_err(|e| match e {
        SwapBuffersError::ContextLost(e) => format!("context lost: {e}"),
        e => format!("swap: {e}"),
    })
}

pub fn init_winit(
    event_loop: &mut EventLoop<Aurora>,
    state: &mut Aurora,
) -> Result<(), Box<dyn std::error::Error>> {
    let (mut backend, winit) = winit::init()?;

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
    // The offscreen frame is upright (as in headless); `BLIT` flips it for the window.
    output.change_current_state(Some(mode), Some(Transform::Normal), None, Some((0, 0).into()));
    output.set_preferred(mode);
    state.space.map_output(&output, (0, 0));

    let mut target = make_target(backend.renderer(), mode.size)?;
    let mut target_size = mode.size;
    backend.window().request_redraw();
    let mut damage_tracker = OutputDamageTracker::from_output(&output);

    event_loop.handle().insert_source(winit, move |event, _, state| {
        match event {
            WinitEvent::Resized { size, .. } => {
                output.change_current_state(Some(Mode { size, refresh: 60_000 }), None, None, None);
                state.update_capture_constraints(&output);
            }
            WinitEvent::Input(event) => state.process_input_event(event),
            WinitEvent::Redraw => {
                let size = backend.window_size();
                if size.w <= 0 || size.h <= 0 {
                    backend.window().request_redraw();
                    return;
                }
                if size != target_size {
                    match make_target(backend.renderer(), size) {
                        Ok(t) => { target = t; target_size = size; }
                        Err(e) => {
                            tracing::error!("winit: cannot resize the offscreen frame, stopping: {e}");
                            state.loop_signal.stop();
                            return;
                        }
                    }
                }
                let drawn = {
                    let renderer = backend.renderer();
                    renderer.bind(&mut target).map(|mut fb| {
                        render::render_and_capture(state, renderer, &mut fb, &output, &mut damage_tracker, size);
                    })
                };
                if let Err(e) = drawn {
                    tracing::error!("winit: cannot bind the offscreen frame, stopping: {e}");
                    state.loop_signal.stop();
                    return;
                }
                if let Err(e) = present(&mut backend, &target, size) {
                    // A lost context cannot be recovered here; leave cleanly rather than panic.
                    tracing::error!("winit: presenting failed, stopping: {e}");
                    state.loop_signal.stop();
                    return;
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
