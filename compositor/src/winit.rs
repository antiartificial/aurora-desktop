//! Winit backend: a real window on the host, for seeing the desktop.
//!
//! It renders the scene straight into the window surface and swaps. There is
//! no offscreen pass, which costs it the effects pipeline and frame capture.
//!
//! Why: binding an offscreen texture makes Smithay call `eglMakeCurrent` with
//! no surface (`GlesRenderer::bind` for a texture, `wait`, `map_texture`,
//! `with_context`). On this host -- NVIDIA 580, KWin, PLATFORM_WAYLAND_KHR --
//! re-binding the window surface afterwards hands out a back buffer that
//! `eglSwapBuffers` does not present. Every call returns success and the
//! window stays blank, so nothing in a log reveals it.
//!
//! This was bisected with `examples/winit_bisect.rs`, which draws a colour
//! four ways: clearing the window directly is visible; the same clear with an
//! offscreen texture bound first is not, and neither is a blit from an
//! offscreen, nor one with an explicit surfaceless `make_current`. Only the
//! phase that never goes surfaceless appears on screen. `examples/winit_red.rs`
//! is the matching baseline: a bare clear-and-swap, which is visible, proving
//! the window path itself works here.
//!
//! So: use `--headless` for effects, capture and the test suites; use winit to
//! look at the thing. Fixing this properly needs a second, shared EGL context
//! so the window's context never goes surfaceless.

use smithay::{
    backend::{
        SwapBuffersError,
        renderer::{damage::OutputDamageTracker, gles::GlesRenderer},
        winit::{self, WinitEvent},
    },
    output::{Mode, Output, PhysicalProperties, Subpixel},
    reexports::calloop::EventLoop,
    utils::{Physical, Rectangle, Transform},
};

use crate::{Aurora, render};

pub fn init_winit(
    event_loop: &mut EventLoop<Aurora>,
    state: &mut Aurora,
) -> Result<(), Box<dyn std::error::Error>> {
    let (mut backend, winit) = winit::init::<GlesRenderer>()?;

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
    // Flipped180 is Smithay's convention for a winit window (anvil does the
    // same): GL's origin is bottom-left, the window's is top-left.
    output.change_current_state(Some(mode), Some(Transform::Flipped180), None, Some((0, 0).into()));
    output.set_preferred(mode);
    state.space.map_output(&output, (0, 0));

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
                let full = Rectangle::<i32, Physical>::from_size(size);
                // One bind, one render, one swap, and nothing in between that
                // could make the context current without the surface.
                let drew = match backend.bind() {
                    Ok((renderer, mut window)) => {
                        render::render_direct(state, renderer, &mut window, &output, &mut damage_tracker, size);
                        true
                    }
                    Err(e) => {
                        tracing::error!("winit: cannot bind the window, stopping: {e}");
                        state.loop_signal.stop();
                        false
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
