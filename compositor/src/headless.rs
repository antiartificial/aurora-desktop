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
) -> Result<(), Box<dyn std::error::Error>> {
    let device = EGLDevice::enumerate()?.next().ok_or("no EGL device found")?;
    let display = unsafe { EGLDisplay::new(device)? };
    let context = EGLContext::new(&display)?;
    let mut renderer = unsafe { GlesRenderer::new(context)? };

    let size: Size<i32, smithay::utils::Physical> = (width, height).into();
    let mut target: GlesTexture =
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
    state.space.map_output(&output, (0, 0));
    let mut tracker = OutputDamageTracker::from_output(&output);

    event_loop.handle().insert_source(Timer::from_duration(Duration::from_millis(16)), move |_, _, state| {
        {
            let mut framebuffer = renderer.bind(&mut target).unwrap();
            render::render_and_capture(state, &mut renderer, &mut framebuffer, &output, &mut tracker, size);
        }
        render::after_frame(state, &output);
        TimeoutAction::ToDuration(Duration::from_millis(16))
    })?;
    Ok(())
}
