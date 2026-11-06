//! Proves, without looking at a screen, that a texture rendered by a second
//! EGL context (B, shared with the window's context A) can be sampled by A:
//! B clears its texture to a known colour, A blits it over the window, then A
//! reads a window pixel back and the result is printed and checked.
//!
//! The read-back goes surfaceless on A, so this runs ONE frame and exits. It
//! says nothing about whether the window presents; only that sharing works.
use smithay::backend::allocator::Fourcc;
use smithay::backend::egl::EGLContext;
use smithay::backend::renderer::gles::{GlesRenderer, GlesTexture};
use smithay::backend::renderer::{Bind, ExportMem, Frame, Offscreen, Renderer};
use smithay::backend::winit::{self, WinitEvent};
use smithay::reexports::calloop::EventLoop;
use smithay::utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform};
use std::time::Duration;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut loopy: EventLoop<Option<bool>> = EventLoop::try_new()?;
    let (mut backend, source) = winit::init::<GlesRenderer>()?;
    let size = backend.window_size();
    let mut b = {
        let a = backend.renderer().egl_context();
        unsafe { GlesRenderer::new(EGLContext::new_shared(a.display(), a)?)? }
    };
    let mut tex: GlesTexture = b.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((size.w, size.h)))?;
    backend.window().request_redraw();
    loopy.handle().insert_source(source, move |event, _, out| {
        let WinitEvent::Redraw = event else { return };
        let full = Rectangle::<i32, Physical>::from_size(size);
        let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
        {
            let mut fb = b.bind(&mut tex).unwrap();
            let mut f = b.render(&mut fb, size, Transform::Normal).unwrap();
            f.clear([0.2, 0.4, 0.8, 1.0].into(), &[full]).unwrap();
            let _ = f.finish().unwrap();
        }
        b.with_context(|gl| unsafe { gl.Finish() }).unwrap();
        let (r, mut win) = backend.bind().unwrap();
        {
            let mut f = r.render(&mut win, size, Transform::Flipped180).unwrap();
            f.render_texture_from_to(&tex, src, full, &[full], &[], Transform::Normal, 1.0, None, &[]).unwrap();
            let _ = f.finish().unwrap();
        }
        let px = Rectangle::<i32, BufferCoords>::new((size.w / 2, size.h / 2).into(), (1, 1).into());
        let m = r.copy_framebuffer(&win, px, Fourcc::Abgr8888).unwrap();
        let bytes = r.map_texture(&m).unwrap().to_vec();
        println!("expected RGB (51,102,204) from B's texture; window pixel read back by A: {:?}", &bytes[..4]);
        *out = Some((bytes[0] as i32 - 51).abs() <= 1 && (bytes[1] as i32 - 102).abs() <= 1 && (bytes[2] as i32 - 204).abs() <= 1);
    })?;
    let mut out = None;
    while out.is_none() { loopy.dispatch(Some(Duration::from_millis(16)), &mut out)?; }
    println!("{}", if out == Some(true) { "SHARED TEXTURE OK" } else { "SHARED TEXTURE MISMATCH" });
    std::process::exit(if out == Some(true) { 0 } else { 1 })
}
