//! Bisection tool, not part of the compositor. Four ways of getting a colour
//! onto the winit window, 3s each, cycling. Each isolates one thing
//! aurora-compositor does that `winit_red` does not:
//!   A RED    clear the window directly               (known good baseline)
//!   B GREEN  bind an offscreen texture first, then clear the window
//!   C BLUE   render into the offscreen, then blit it to the window
//!   D YELLOW as C, plus a surfaceless make_current between render and blit
//!   E CYAN   the compositor's two-context path: a SECOND EGL context, shared
//!            with the window's, renders the colour into its own texture and
//!            finishes; the window's context then binds the window, draws that
//!            one texture and swaps. The window's context never goes surfaceless.
//!   F MAGENTA as E, but it survives the window being RESIZED: context B's
//!            target is recreated at the new size every time it changes, and
//!            the frame is magenta with a 40px WHITE border. Resize the window
//!            (drag, or a KWin script) while it runs. Expect: magenta fills
//!            the window with an unbroken white border at every size and the
//!            window never goes blank. Border cut off or missing = target size
//!            wrong; blank = resize broke presenting. (Not in the default
//!            cycle: BISECT_PHASES=F, or AEF.)
//! Expect: A RED visible; B, C, D known blank on this host; E CYAN visible iff
//! the two-context design presents. (Phases B-D are the evidence, not a goal.)
//! Cycle order is A E B C D (BISECT_PHASES=AE runs only those, e.g. just the
//! baseline and the new path).
//! Whichever phase goes black is the operation that breaks presenting.
use smithay::backend::egl::EGLContext;
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
    // Context B, shared with the window's context A, and its own target.
    let mut renderer_b = {
        let a = backend.renderer().egl_context();
        let ctx = EGLContext::new_shared(a.display(), a)?;
        unsafe { GlesRenderer::new(ctx)? }
    };
    let mut size_b = size0;
    let mut target_b: GlesTexture = renderer_b.create_buffer(
        Fourcc::Abgr8888,
        Size::<i32, BufferCoords>::from((size0.w.max(1), size0.h.max(1))),
    )?;
    // Cycle order A, E, B, C, D: E straight after the known-good A, so it is not
    // judged after B-D have gone surfaceless. BISECT_PHASES=AE restricts the set.
    let want = std::env::var("BISECT_PHASES").unwrap_or_else(|_| "AEBCD".into());
    let order: Vec<usize> = want.chars().filter_map(|c| "ABCDEF".find(c)).collect();
    assert!(!order.is_empty(), "BISECT_PHASES must name some of A-F");
    let start = Instant::now();
    let mut last = 9usize;
    backend.window().request_redraw();
    loopy.handle().insert_source(source, move |event, _, stop| {
        if let WinitEvent::CloseRequested = event { *stop = true; return; }
        let WinitEvent::Redraw = event else { return };
        let size = backend.window_size();
        let k = (start.elapsed().as_secs() / 3) as usize % order.len();
        let phase = order[k];
        if size.w <= 0 || size.h <= 0 || (size != size0 && phase != 5) { backend.window().request_redraw(); return; }
        let full = Rectangle::<i32, Physical>::from_size(size);
        let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
        let (name, col) = match phase {
            0 => ("A RED    window clear only", [1.0, 0.0, 0.0, 1.0f32]),
            1 => ("B GREEN  offscreen bound first, then window clear", [0.0, 1.0, 0.0, 1.0]),
            2 => ("C BLUE   rendered offscreen, blitted to window", [0.0, 0.0, 1.0, 1.0]),
            3 => ("D YELLOW as C plus a surfaceless make_current", [1.0, 1.0, 0.0, 1.0]),
            5 => ("F MAGENTA like E, target recreated on resize; white border", [1.0, 0.0, 1.0, 1.0]),
            _ => ("E CYAN   second shared context renders, window context only blits", [0.0, 1.0, 1.0, 1.0]),
        };
        if phase != last { println!("--- phase {name}"); last = phase; }

        if phase == 4 || phase == 5 {
            if size != size_b {
                if let Ok(t) = renderer_b.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((size.w, size.h))) {
                    target_b = t; size_b = size;
                }
            }
            if size_b != size { backend.window().request_redraw(); return; }
            if let Ok(mut fb) = renderer_b.bind(&mut target_b) {
                if let Ok(mut f) = renderer_b.render(&mut fb, size, Transform::Normal) {
                    if phase == 5 {
                        let _ = f.clear([1.0, 1.0, 1.0, 1.0].into(), &[full]);
                        let inner = Rectangle::<i32, Physical>::new((40, 40).into(), (size.w - 80, size.h - 80).into());
                        let _ = f.clear(col.into(), &[inner]);
                    } else {
                        let _ = f.clear(col.into(), &[full]);
                    }
                    let _ = f.finish();
                }
            }
            let _ = renderer_b.with_context(|gl| unsafe { gl.Finish() });
        } else if phase >= 1 && phase < 4 {
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
                    else if phase >= 4 { let _ = f.render_texture_from_to(&target_b, src, full, &[full], &[], Transform::Normal, 1.0, None, &[]); }
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
