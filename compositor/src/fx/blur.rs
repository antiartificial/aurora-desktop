//! Dual-Kawase blur: a chain of downsamples, each half the size of the last,
//! then the same number of upsamples back to full size. Texture `down[k-1]`
//! is level k; `up[k]` is level k (so `up[0]` is the full-size result).

use smithay::{
    backend::renderer::{
        Bind, Frame, Renderer, Texture,
        gles::{GlesRenderer, GlesTexProgram, GlesTexture, Uniform},
    },
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform},
};

use super::{Res, Scratch, theme::Glass, wait};

fn pass(r: &mut GlesRenderer, prog: &GlesTexProgram, src: &GlesTexture, dst: &mut GlesTexture, offset: f32) -> Res<()> {
    let (ss, ds) = (src.size(), dst.size());
    let size = Size::<i32, Physical>::from((ds.w, ds.h));
    let full = Rectangle::<i32, Physical>::from_size(size);
    let u = [Uniform::new("halfpixel", (0.5 / ss.w as f32, 0.5 / ss.h as f32)), Uniform::new("offset", offset)];
    let mut t = r.bind(dst)?;
    let mut f = r.render(&mut t, size, Transform::Normal)?;
    let srcr = Rectangle::<f64, BufferCoords>::from_size((ss.w as f64, ss.h as f64).into());
    f.render_texture_from_to(src, srcr, full, &[full], &[], Transform::Normal, 1.0, Some(prog), &u)?;
    let sy = f.finish()?;
    wait(r, sy)
}

/// Blur `s.scene` into `s.up[0]`.
pub fn run(r: &mut GlesRenderer, s: &mut Scratch, down: &GlesTexProgram, up: &GlesTexProgram, g: &Glass, _size: Size<i32, Physical>) -> Res<()> {
    let n = s.passes;
    let off = g.blur_radius;
    pass(r, down, &s.scene, &mut s.down[0], off)?;
    for k in 1..n {
        let (a, b) = s.down.split_at_mut(k);
        pass(r, down, &a[k - 1], &mut b[0], off)?;
    }
    pass(r, up, &s.down[n - 1], &mut s.up[n - 1], off)?;
    for k in (0..n - 1).rev() {
        let (a, b) = s.up.split_at_mut(k + 1);
        pass(r, up, &b[0], &mut a[k], off)?;
    }
    Ok(())
}
