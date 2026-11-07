//! Headless backend: no host window. A surfaceless EGL context and an offscreen
//! texture stand in for the winit window, and a timer stands in for the host's
//! frame callbacks. Capture goes through the same `render_and_capture` as the
//! winit backend (which also renders offscreen).

use std::time::Duration;

use smithay::{
    backend::{
        allocator::Fourcc,
        egl::{EGLContext, EGLDisplay, EGLDevice},
        renderer::{Bind, Offscreen, damage::OutputDamageTracker, gles::{GlesRenderer, GlesTexture}},
    },
    output::{Mode, Output, PhysicalProperties, Subpixel},
    reexports::calloop::{EventLoop, timer::{TimeoutAction, Timer}},
    utils::{Buffer as BufferCoords, Size, Transform},
};

use crate::{Aurora, render};

pub fn init_headless(
    event_loop: &mut EventLoop<Aurora>,
    state: &mut Aurora,
    width: i32,
    height: i32,
    scale: f64,
    extra: &[(i32, i32)],
) -> Result<(), Box<dyn std::error::Error>> {
    let device = EGLDevice::enumerate()?.next().ok_or("no EGL device found")?;
    let display = unsafe { EGLDisplay::new(device)? };
    let context = EGLContext::new(&display)?;
    let mut renderer = unsafe { GlesRenderer::new(context)? };

    let size: Size<i32, smithay::utils::Physical> = (width, height).into();
    let target: GlesTexture =
        renderer.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((width, height)))?;

    let mode = Mode { size, refresh: 60_000 };
    let output = Output::new(
        "headless".to_string(),
        PhysicalProperties {
            size: (0, 0).into(),
            subpixel: Subpixel::Unknown,
            make: "Aurora".into(),
            model: "Headless".into(),
            serial_number: "Unknown".into(),
        },
    );
    let _global = output.create_global::<Aurora>(&state.display_handle);
    output.change_current_state(Some(mode), Some(Transform::Normal), None, Some((0, 0).into()));
    output.set_preferred(mode);
    state.set_host_scale(&output, scale);
    state.space.map_output(&output, (0, 0));

    // Each output gets its own offscreen target and its own damage tracker: they are different
    // surfaces with different contents, and sharing a tracker between them would have each
    // telling the other which pixels were already clean.
    //
    // Laid out left to right, which is what `extra` means and what a person plugging in a second
    // monitor expects without being asked. The first output keeps the origin, so with ONE output
    // every coordinate below is unchanged from before this existed.
    let first_tracker = OutputDamageTracker::from_output(&output);
    let mut outs: Vec<(Output, GlesTexture, OutputDamageTracker, Size<i32, smithay::utils::Physical>)> =
        vec![(output, target, first_tracker, size)];
    let mut x = width;
    for (i, &(w, h)) in extra.iter().enumerate() {
        let sz: Size<i32, smithay::utils::Physical> = (w, h).into();
        let t: GlesTexture = renderer.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((w, h)))?;
        let m = Mode { size: sz, refresh: 60_000 };
        let o = Output::new(
            format!("headless-{}", i + 2),
            PhysicalProperties { size: (0, 0).into(), subpixel: Subpixel::Unknown, make: "Aurora".into(), model: "Headless".into(), serial_number: "Unknown".into() },
        );
        let g = o.create_global::<Aurora>(&state.display_handle);
        std::mem::forget(g);
        o.change_current_state(Some(m), Some(Transform::Normal), None, Some((x, 0).into()));
        o.set_preferred(m);
        state.set_host_scale(&o, scale);
        state.space.map_output(&o, (x, 0));
        let dt = OutputDamageTracker::from_output(&o);
        outs.push((o, t, dt, sz));
        x += w;
    }
    tracing::info!("headless: {} output(s)", outs.len());

    event_loop.handle().insert_source(Timer::from_duration(Duration::from_millis(16)), move |_, _, state| {
        for (o, target, tracker, sz) in outs.iter_mut() {
            {
                let mut framebuffer = renderer.bind(target).unwrap();
                render::render_and_capture(state, &mut renderer, &mut framebuffer, o, tracker, *sz);
            }
            render::after_frame(state, o);
        }
        TimeoutAction::ToDuration(Duration::from_millis(16))
    })?;
    Ok(())
}
