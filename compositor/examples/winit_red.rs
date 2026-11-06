//! Bisection tool, not part of the compositor. Clears the winit window to red
//! and swaps. Nothing else: no offscreen texture, no effects, no capture, no
//! Wayland server. If this window is red, presenting to a winit window works
//! on this host and the fault is in what aurora-compositor does around it. If
//! it is blank, the window path itself is broken.
use smithay::backend::renderer::{Frame, Renderer};
use smithay::backend::winit::{self, WinitEvent};
use smithay::reexports::calloop::EventLoop;
use smithay::utils::{Physical, Rectangle, Transform};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt().init();
    let mut loopy: EventLoop<bool> = EventLoop::try_new()?;
    let (mut backend, source) = winit::init::<smithay::backend::renderer::gles::GlesRenderer>()?;
    let mut frames = 0u32;
    backend.window().request_redraw();
    loopy.handle().insert_source(source, move |event, _, stop| {
        if let WinitEvent::Redraw = event {
            let size = backend.window_size();
            let full = Rectangle::<i32, Physical>::from_size(size);
            let drew = {
                match backend.bind() {
                    Ok((renderer, mut window)) => match renderer.render(&mut window, size, Transform::Normal) {
                        Ok(mut f) => {
                            let c = f.clear([1.0, 0.0, 0.0, 1.0].into(), &[full]);
                            let d = f.finish();
                            if let Err(e) = c { println!("clear failed: {e}"); }
                            if let Err(e) = d { println!("finish failed: {e}"); }
                            true
                        }
                        Err(e) => { println!("render failed: {e}"); false }
                    },
                    Err(e) => { println!("bind failed: {e}"); false }
                }
            };
            if drew {
                match backend.submit(Some(&[full])) {
                    Ok(()) => {
                        frames += 1;
                        if frames % 60 == 1 {
                            println!("frame {frames}: cleared {}x{} red and swapped OK", size.w, size.h);
                        }
                    }
                    Err(e) => println!("submit failed: {e}"),
                }
            }
            backend.window().request_redraw();
        }
        if let WinitEvent::CloseRequested = event {
            *stop = true;
        }
    })?;
    let mut stop = false;
    while !stop {
        loopy.dispatch(Some(std::time::Duration::from_millis(16)), &mut stop)?;
    }
    Ok(())
}
