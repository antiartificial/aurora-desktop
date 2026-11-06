//! `--dump-overlay PATH`: the compositor writes its own final frame, overlay
//! included, as a PNG. Seen by the compositor, not through any client.
//!
//! This is a deliberate read channel for the overlay, opened by whoever
//! starts the compositor with the flag, readable by whoever can read PATH.
//! It is for demos and tests (it is how the tests prove the overlay was
//! really drawn). A compositor run for real use must not pass it.

use std::path::Path;

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{ExportMem, TextureMapping, gles::{GlesRenderer, GlesTarget}},
    },
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size},
};

pub fn write_frame(
    renderer: &mut GlesRenderer,
    framebuffer: &GlesTarget<'_>,
    size: Size<i32, Physical>,
    path: &Path,
) -> Result<(), Box<dyn std::error::Error>> {
    let region = Rectangle::<i32, BufferCoords>::from_size((size.w, size.h).into());
    let m = renderer.copy_framebuffer(framebuffer, region, Fourcc::Abgr8888)?;
    let flipped = m.flipped();
    let px = renderer.map_texture(&m)?.to_vec();
    let (w, h) = (size.w as usize, size.h as usize);
    let mut rows: Vec<u8> = Vec::with_capacity(w * h * 4);
    for y in 0..h {
        let sy = if flipped { y } else { h - 1 - y };
        rows.extend_from_slice(&px[sy * w * 4..(sy + 1) * w * 4]);
    }
    for a in rows.chunks_exact_mut(4) {
        a[3] = 255;
    }
    let tmp = path.with_extension("tmp.png");
    let mut enc = png::Encoder::new(std::io::BufWriter::new(std::fs::File::create(&tmp)?), w as u32, h as u32);
    enc.set_color(png::ColorType::Rgba);
    enc.set_depth(png::BitDepth::Eight);
    enc.write_header()?.write_image_data(&rows)?;
    std::fs::rename(tmp, path)?;
    Ok(())
}
