//! Bisection tool, not part of the compositor. Four ways of getting a colour
//! onto the winit window, 3s each, cycling. Each isolates one thing
//! aurora-compositor does that `winit_red` does not:
//!   A RED    clear the window directly               (known good baseline)
//!   B GREEN  bind an offscreen texture first, then clear the window
//!   C BLUE   render into the offscreen, then blit it to the window
//!   D YELLOW as C, plus a surfaceless make_current between render and blit
//! Whichever phase goes black is the operation that breaks presenting.
use smithay::backend::renderer::gles::{GlesRenderer, GlesTexture};
use smithay::backend::renderer::{Bind, Frame, Offscreen, Renderer};
use smithay::backend::allocator::Fourcc;
use smithay::backend::winit::{self, WinitEvent};
use smithay::reexports::calloop::EventLoop;
use smithay::utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform};
use std::time::{Duration, Instant};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt().init();
    let mut loopy: EventLoop<bool> = EventLoop::try_new()?;
    let (mut backend, source) = winit::init::<GlesRenderer>()?;
    let size0 = backend.window_size();
    let mut target: GlesTexture = backend.renderer().create_buffer(
        Fourcc::Abgr8888,
        Size::<i32, BufferCoords>::from((size0.w.max(1), size0.h.max(1))),
    )?;
    let start = Instant::now();
    let mut last = 9usize;
    backend.window().request_redraw();
    loopy.handle().insert_source(source, move |event, _, stop| {
        if let WinitEvent::CloseRequested = event { *stop = true; return; }
        let WinitEvent::Redraw = event else { return };
        let size = backend.window_size();
        if size != size0 { backend.window().request_redraw(); return; }
        let full = Rectangle::<i32, Physical>::from_size(size);
        let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
        let phase = (start.elapsed().as_secs() / 3) as usize % 4;
        let (name, col) = match phase {
            0 => ("A RED    window clear only", [1.0, 0.0, 0.0, 1.0f32]),
            1 => ("B GREEN  offscreen bound first, then window clear", [0.0, 1.0, 0.0, 1.0]),
            2 => ("C BLUE   rendered offscreen, blitted to window", [0.0, 0.0, 1.0, 1.0]),
            _ => ("D YELLOW as C plus a surfaceless make_current", [1.0, 1.0, 0.0, 1.0]),
        };
        if phase != last { println!("--- phase {name}"); last = phase; }

        if phase >= 1 {
            // touch the offscreen exactly as the compositor does
            let r = backend.renderer();
            if let Ok(mut fb) = r.bind(&mut target) {
                if let Ok(mut f) = r.render(&mut fb, size, Transform::Normal) {
                    let _ = f.clear(col.into(), &[full]);
                    let _ = f.finish();
                }
            }
        }
        if phase == 3 {
            let _ = backend.renderer().with_context(|_| ()); // surfaceless make_current
        }
        let ok = match backend.bind() {
            Ok((renderer, mut window)) => match renderer.render(&mut window, size, Transform::Flipped180) {
                Ok(mut f) => {
                    if phase <= 1 { let _ = f.clear(col.into(), &[full]); }
                    else { let _ = f.render_texture_from_to(&target, src, full, &[full], &[], Transform::Normal, 1.0, None, &[]); }
                    f.finish().is_ok()
                }
                Err(e) => { println!("  render: {e}"); false }
            },
            Err(e) => { println!("  bind: {e}"); false }
        };
        if ok { if let Err(e) = backend.submit(Some(&[full])) { println!("  submit: {e}"); } }
        backend.window().request_redraw();
    })?;
    let mut stop = false;
    while !stop { loopy.dispatch(Some(Duration::from_millis(16)), &mut stop)?; }
    Ok(())
}
