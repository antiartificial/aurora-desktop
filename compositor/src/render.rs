//! One frame: render the space into the bound framebuffer, fill queued capture
//! frames from it, then send frame callbacks. Shared by the winit and headless
//! backends so both go through the same capture path.

use std::time::{Duration, Instant};

use smithay::{
    backend::renderer::{
        damage::OutputDamageTracker,
        element::surface::WaylandSurfaceRenderElement,
        gles::{GlesRenderer, GlesTarget},
    },
    output::Output,
    utils::{Physical, Size},
};

use crate::{Aurora, stats::{Phase, Sample, ms}};

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
    let frame_start = Instant::now();
    let mut sample = Sample::default();
    state.fx.poll(renderer);
    state.sync_scale(output);
    state.shell.sync(&state.fx.theme);
    let specs = if state.shell.deco_on() { state.deco_specs() } else { vec![] };
    state.shell.set_decos(specs);
    state.deskbar_frame();
    state.apply_input_config();
    if state.overlay_up() {
        state.fx.switcher.open = false;
    }
    let mut done = false;
    if state.fx.active(&state.shell) {
        state.fx.phases.begin();
        let t = Instant::now();
        let r = state.fx.render_scene(renderer, framebuffer, output, size, &state.space, &mut state.shell);
        if state.stats.is_some() {
            let p = &state.fx.phases;
            sample = Sample {
                effects: ms(t.elapsed()), scene: p.ms(Phase::Scene), prepass: p.ms(Phase::Prepass), chain: p.ms(Phase::Chain),
                composite: p.ms(Phase::Composite), switcher: p.ms(Phase::Switcher), blit: p.ms(Phase::Blit),
                surfaces: p.surfaces, prepasses: p.prepasses, fx: r.is_ok(), ..Sample::default()
            };
        }
        match r {
            Ok(()) => done = true,
            Err(e) => tracing::error!("effects pipeline failed, drawing flat this frame: {e}"),
        }
    }
    if !done {
        if let Err(e) = smithay::desktop::space::render_output::<_, WaylandSurfaceRenderElement<GlesRenderer>, _, _>(
            output,
            renderer,
            framebuffer,
            1.0,
            0, // buffer age 0: always repaint everything, so a capture sees a full frame
            [&state.space],
            &[],
            tracker,
            CLEAR,
        ) {
            tracing::error!("scene render failed: {e:?}");
        }
    }
    state.serve_pending(renderer, framebuffer, size, state.start_time.elapsed());
    // The trusted overlay goes on last, after captures were served from the
    // framebuffer above, so no capture can contain it.
    let overlay_start = Instant::now();
    match crate::trusted::draw::draw_overlay(&mut state.trusted, &mut state.shell.text, renderer, framebuffer, output, size) {
        Ok(true) => dump_once(state, renderer, framebuffer, size),
        Ok(false) => {}
        Err(e) => tracing::error!("trusted overlay draw failed: {e}"),
    }
    // The indicator goes on after the overlay: the scrim must not dim the evidence. Also after the capture.
    draw_indicator(state, renderer, framebuffer, output, size);
    if state.stats.is_some() {
        sample.overlay = ms(overlay_start.elapsed());
        sample.total = ms(frame_start.elapsed());
        let frame = state.frame;
        if let Some(st) = state.stats.as_mut() {
            st.push(frame, sample);
        }
    }
}

/// Render the scene and the trusted overlay straight into `framebuffer`, with
/// no offscreen pass and no capture.
///
/// This exists for the winit backend only. Binding an offscreen texture calls
/// `eglMakeCurrent` with no surface, and on this NVIDIA driver the window
/// surface then hands out a back buffer that `eglSwapBuffers` does not
/// present: every call reports success and the window stays blank. The
/// effects pipeline and capture read-back both do exactly that, so winit gets
/// neither. Headless keeps both; see `render_and_capture`.
pub fn render_direct(
    state: &mut Aurora,
    renderer: &mut GlesRenderer,
    framebuffer: &mut GlesTarget<'_>,
    output: &Output,
    tracker: &mut OutputDamageTracker,
    size: Size<i32, Physical>,
) {
    state.fail_pending();
    if let Err(e) = smithay::desktop::space::render_output::<_, WaylandSurfaceRenderElement<GlesRenderer>, _, _>(
        output, renderer, framebuffer, 1.0, 0, [&state.space], &[], tracker, CLEAR,
    ) {
        tracing::error!("scene render failed: {e:?}");
    }
    if let Err(e) = crate::trusted::draw::draw_overlay(&mut state.trusted, &mut state.shell.text, renderer, framebuffer, output, size) {
        tracing::error!("trusted overlay draw failed: {e}");
    }
    draw_indicator(state, renderer, framebuffer, output, size);
}

/// The indicator, last of all. A `dump` request (test input) is answered from the same final frame.
fn draw_indicator(state: &mut Aurora, renderer: &mut GlesRenderer, framebuffer: &mut GlesTarget<'_>, output: &Output, size: Size<i32, Physical>) {
    let cfg = state.fx.theme.indicator.clone();
    if let Err(e) = crate::indicator::draw::draw_indicator(&mut state.indicator, &cfg, &mut state.shell.text, renderer, framebuffer, output, size) {
        tracing::error!("indicator draw failed: {e}");
    }
    if let Some(path) = state.indicator.dump.take() {
        if let Err(e) = crate::trusted::dump::write_frame(renderer, framebuffer, size, &path) {
            tracing::error!("frame dump failed: {e}");
        }
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
    state.frame += 1;
    let frame = state.frame;
    state.frame_waiters.retain_mut(|(s, until)| {
        if frame < *until { return true; }
        use std::io::Write;
        let _ = writeln!(s, "ok");
        false
    });
    let now: Duration = state.start_time.elapsed();
    state.space.elements().for_each(|window| {
        window.send_frame(output, now, Some(Duration::ZERO), |_, _| Some(output.clone()))
    });
    state.space.refresh();
    state.trusted_tick();
    state.shell.tick();
    state.deskbar_tick();
    state.persist_tick();
    state.popups.cleanup();
    state.refocus_after_popup();
    let _ = state.display_handle.flush_clients();
}
