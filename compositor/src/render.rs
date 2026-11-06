//! One frame: render the space into the bound framebuffer, fill queued capture
//! frames from it, then send frame callbacks. Shared by the winit and headless
//! backends so both go through the same capture path.

use std::time::Duration;

use smithay::{
    backend::renderer::{
        damage::OutputDamageTracker,
        element::surface::WaylandSurfaceRenderElement,
        gles::{GlesRenderer, GlesTarget},
    },
    output::Output,
    utils::{Physical, Size},
};

use crate::Aurora;

/// Background colour behind windows.
pub const CLEAR: [f32; 4] = [0.1, 0.1, 0.1, 1.0];

/// Render into `framebuffer` and serve pending captures from it.
pub fn render_and_capture(
    state: &mut Aurora,
    renderer: &mut GlesRenderer,
    framebuffer: &mut GlesTarget<'_>,
    output: &Output,
    tracker: &mut OutputDamageTracker,
    size: Size<i32, Physical>,
) {
    smithay::desktop::space::render_output::<_, WaylandSurfaceRenderElement<GlesRenderer>, _, _>(
        output,
        renderer,
        framebuffer,
        1.0,
        0, // buffer age 0: always repaint everything, so a capture sees a full frame
        [&state.space],
        &[],
        tracker,
        CLEAR,
    )
    .unwrap();
    state.serve_pending(renderer, framebuffer, size, state.start_time.elapsed());
}

/// Frame callbacks and housekeeping after a frame was presented.
pub fn after_frame(state: &mut Aurora, output: &Output) {
    let now: Duration = state.start_time.elapsed();
    state.space.elements().for_each(|window| {
        window.send_frame(output, now, Some(Duration::ZERO), |_, _| Some(output.clone()))
    });
    state.space.refresh();
    state.popups.cleanup();
    let _ = state.display_handle.flush_clients();
}
