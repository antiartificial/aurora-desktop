//! ext-image-copy-capture-v1: sessions stay alive, frames are queued and then
//! filled from the real rendered framebuffer on the next redraw.
//!
//! Anvil stops the session right after offering constraints and fails every
//! frame. This does the reverse: `new_session` stores the `Session`, `frame`
//! queues the `Frame`, and `serve_pending` reads the output's framebuffer back
//! with `ExportMem::copy_framebuffer`, writes it into the client's shm buffer
//! and only then calls `Frame::success`.

use std::time::Duration;

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{ExportMem, TextureMapping, gles::{GlesRenderer, GlesTarget}},
    },
    output::{Output, WeakOutput},
    reexports::wayland_server::protocol::wl_shm,
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform},
    wayland::{
        image_capture_source::{
            ImageCaptureSource, ImageCaptureSourceHandler, OutputCaptureSourceHandler,
            OutputCaptureSourceState,
        },
        image_copy_capture::{
            BufferConstraints, CaptureFailureReason, Frame, FrameRef, ImageCopyCaptureHandler,
            ImageCopyCaptureState, Session, SessionRef,
        },
        shm::with_buffer_contents_mut,
    },
};

use crate::Aurora;

impl ImageCaptureSourceHandler for Aurora {}

impl OutputCaptureSourceHandler for Aurora {
    fn output_capture_source_state(&mut self) -> &mut OutputCaptureSourceState {
        &mut self.output_capture_source_state
    }

    fn output_source_created(&mut self, source: ImageCaptureSource, output: &Output) {
        source.user_data().insert_if_missing(|| output.downgrade());
    }
}

/// Constraints for an output source: the output's current mode, shm only.
pub fn output_constraints(output: &Output) -> Option<BufferConstraints> {
    let mode = output.current_mode()?;
    Some(BufferConstraints {
        size: mode.size.to_logical(1).to_buffer(1, Transform::Normal),
        shm: vec![wl_shm::Format::Xrgb8888, wl_shm::Format::Argb8888],
    })
}

impl ImageCopyCaptureHandler for Aurora {
    fn image_copy_capture_state(&mut self) -> &mut ImageCopyCaptureState {
        &mut self.image_copy_capture_state
    }

    fn capture_constraints(&mut self, source: &ImageCaptureSource) -> Option<BufferConstraints> {
        let output = source.user_data().get::<WeakOutput>()?.upgrade()?;
        output_constraints(&output)
    }

    fn new_session(&mut self, session: Session) {
        // Holding the Session keeps it alive; dropping it would stop it.
        self.capture_sessions.push(session);
    }

    fn frame(&mut self, session: &SessionRef, frame: Frame) {
        self.pending_frames.push((session.clone(), frame));
    }

    fn frame_aborted(&mut self, frame: FrameRef) {
        self.pending_frames.retain(|(_, f)| *f != frame);
    }

    fn session_destroyed(&mut self, session: SessionRef) {
        self.capture_sessions.retain(|s| *s != session);
        self.pending_frames.retain(|(s, _)| *s != session);
    }
}

impl Aurora {
    /// Tell every live session the output size changed.
    pub fn update_capture_constraints(&mut self, output: &Output) {
        if let Some(c) = output_constraints(output) {
            for s in &self.capture_sessions {
                s.update_constraints(c.clone());
            }
        }
    }

    /// Fill every queued frame from `framebuffer`, which must hold the output's
    /// freshly rendered content. Frames that cannot be filled are failed with a
    /// reason, never completed blank.
    pub fn serve_pending(
        &mut self,
        renderer: &mut GlesRenderer,
        framebuffer: &GlesTarget<'_>,
        size: Size<i32, Physical>,
        presented: Duration,
    ) {
        if self.pending_frames.is_empty() {
            return;
        }
        let pending = std::mem::take(&mut self.pending_frames);
        let region = Rectangle::<i32, BufferCoords>::from_size((size.w, size.h).into());
        // Abgr8888 is R,G,B,A in memory: the format every GL read-back supports.
        let read = renderer
            .copy_framebuffer(framebuffer, region, Fourcc::Abgr8888)
            .and_then(|m| {
                let flipped = m.flipped();
                renderer.map_texture(&m).map(|b| (b.to_vec(), flipped))
            });
        let (pixels, gl_flipped) = match read {
            Ok(r) => r,
            Err(e) => {
                tracing::error!("framebuffer read-back failed: {e:?}");
                for (_, f) in pending {
                    f.fail(CaptureFailureReason::Unknown);
                }
                return;
            }
        };
        for (session, frame) in pending {
            // The session's constraints may have changed since the frame was queued.
            let ok = session
                .current_constraints()
                .is_some_and(|c| c.size.w == size.w && c.size.h == size.h);
            if !ok {
                frame.fail(CaptureFailureReason::BufferConstraints);
                continue;
            }
            match write_shm(&frame, &pixels, size, gl_flipped) {
                Ok(()) => {
                    let full = vec![Rectangle::from_size((size.w, size.h).into())];
                    frame.success(Transform::Normal, Some(full), presented);
                }
                Err(e) => {
                    tracing::warn!("capture frame failed: {e}");
                    frame.fail(CaptureFailureReason::BufferConstraints);
                }
            }
        }
    }
}

/// Copy RGBA rows into the frame's shm buffer as XRGB/ARGB8888 (B,G,R,A bytes),
/// top row first.
fn write_shm(
    frame: &Frame,
    rgba: &[u8],
    size: Size<i32, Physical>,
    gl_flipped: bool,
) -> Result<(), String> {
    let (w, h) = (size.w as usize, size.h as usize);
    with_buffer_contents_mut(&frame.buffer(), |ptr, len, data| {
        let (stride, offset) = (data.stride as usize, data.offset as usize);
        let fmt_ok = matches!(data.format, wl_shm::Format::Xrgb8888 | wl_shm::Format::Argb8888);
        if !fmt_ok || (data.width as usize) < w || (data.height as usize) < h || stride < w * 4 {
            return Err("buffer does not meet the advertised constraints".to_string());
        }
        if offset + stride * (h - 1) + w * 4 > len {
            return Err("buffer extends past its pool".to_string());
        }
        // GL reads bottom row first; `flipped()` says whether rows are already top-down.
        let reverse = !gl_flipped;
        for y in 0..h {
            let src_y = if reverse { h - 1 - y } else { y };
            let src = &rgba[src_y * w * 4..(src_y + 1) * w * 4];
            // SAFETY: bounds checked above; the pointer is valid inside this callback.
            let dst = unsafe { std::slice::from_raw_parts_mut(ptr.add(offset + y * stride), w * 4) };
            for (d, s) in dst.chunks_exact_mut(4).zip(src.chunks_exact(4)) {
                d.copy_from_slice(&[s[2], s[1], s[0], 255]);
            }
        }
        Ok(())
    })
    .map_err(|e| format!("{e:?}"))?
}
