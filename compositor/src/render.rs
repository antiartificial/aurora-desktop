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
    state.fx.poll(renderer);
    if state.overlay_up() {
        state.fx.switcher.open = false;
    }
    let mut done = false;
    if state.fx.active() {
        match state.fx.render_scene(renderer, framebuffer, output, size, &state.space) {
            Ok(()) => done = true,
            Err(e) => tracing::error!("effects pipeline failed, drawing flat this frame: {e}"),
        }
    }
    if !done {
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
    }
    state.serve_pending(renderer, framebuffer, size, state.start_time.elapsed());
    // The trusted overlay goes on last, after captures were served from the
    // framebuffer above, so no capture can contain it.
    match crate::trusted::draw::draw_overlay(&mut state.trusted, renderer, framebuffer, output, size) {
        Ok(true) => dump_once(state, renderer, framebuffer, size),
        Ok(false) => {}
        Err(e) => tracing::error!("trusted overlay draw failed: {e}"),
    }
}

/// `--dump-overlay`: write the compositor's own frame once per request.
fn dump_once(state: &mut Aurora, renderer: &mut GlesRenderer, framebuffer: &GlesTarget<'_>, size: Size<i32, Physical>) {
    let (Some(path), Some(head)) = (state.trusted.dump_path.clone(), state.trusted.queue.front()) else { return };
    if state.trusted.dumped == Some(head.id) {
        return;
    }
    state.trusted.dumped = Some(head.id);
    if let Err(e) = crate::trusted::dump::write_frame(renderer, framebuffer, size, &path) {
        tracing::error!("overlay dump failed: {e}");
    }
}

/// Frame callbacks and housekeeping after a frame was presented.
pub fn after_frame(state: &mut Aurora, output: &Output) {
    let now: Duration = state.start_time.elapsed();
    state.space.elements().for_each(|window| {
        window.send_frame(output, now, Some(Duration::ZERO), |_, _| Some(output.clone()))
    });
    state.space.refresh();
    state.trusted_tick();
    state.popups.cleanup();
    let _ = state.display_handle.flush_clients();
}
