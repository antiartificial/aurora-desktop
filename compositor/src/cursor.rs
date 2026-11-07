//! What the pointer should look like, and the one place that decides it.
//!
//! A window you can resize from its edges is only resizable if you can TELL. The grip is eight
//! logical pixels outside the frame; without the pointer changing shape there, the affordance is
//! invisible and people drag the title bar instead and conclude the window is fixed. That was the
//! review note that produced this file: "the mouse cursor should change to signify actions that
//! can be taken on the appropriate edge thresholds."
//!
//! The shape is derived from what is already hit-tested, not tracked separately, so it cannot
//! disagree with what a click would do -- which is the failure mode that makes cursor feedback
//! worse than none.

use smithay::reexports::winit::cursor::CursorIcon;

use crate::grabs::resize_grab::ResizeEdge;

/// The shapes this compositor uses. Deliberately few: every one has to mean something a user can
/// act on, and a cursor that changes for reasons nobody can name is noise.
#[derive(Copy, Clone, PartialEq, Eq, Debug, Default)]
pub enum Shape {
    #[default]
    Default,
    /// Over a thing that responds to a click: a close button, a dock item.
    Pointer,
    /// Over a title bar, or while a move grab runs.
    Move,
    Resize(Side),
}

#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum Side {
    N,
    S,
    E,
    W,
    NE,
    NW,
    SE,
    SW,
}

impl Shape {
    /// The resize shape for a set of edges, or `Default` when the set names none.
    ///
    /// A corner is two edges at once, which is why this takes the set rather than one edge: the
    /// corner regions are the ones people actually aim for, and offering them a single-axis
    /// arrow would be a lie about what the drag will do.
    pub fn for_edges(e: ResizeEdge) -> Shape {
        let top = e.contains(ResizeEdge::TOP);
        let bottom = e.contains(ResizeEdge::BOTTOM);
        let left = e.contains(ResizeEdge::LEFT);
        let right = e.contains(ResizeEdge::RIGHT);
        let side = match (top, bottom, left, right) {
            (true, _, true, _) => Side::NW,
            (true, _, _, true) => Side::NE,
            (_, true, true, _) => Side::SW,
            (_, true, _, true) => Side::SE,
            (true, ..) => Side::N,
            (_, true, ..) => Side::S,
            (_, _, true, _) => Side::W,
            (_, _, _, true) => Side::E,
            _ => return Shape::Default,
        };
        Shape::Resize(side)
    }

    /// The winit icon for this shape. Only the nested backend uses this; on DRM the compositor
    /// draws its own pointer and wants a bitmap instead, which is not built.
    pub fn icon(self) -> CursorIcon {
        match self {
            Shape::Default => CursorIcon::Default,
            Shape::Pointer => CursorIcon::Pointer,
            Shape::Move => CursorIcon::Move,
            Shape::Resize(s) => match s {
                Side::N => CursorIcon::NResize,
                Side::S => CursorIcon::SResize,
                Side::E => CursorIcon::EResize,
                Side::W => CursorIcon::WResize,
                Side::NE => CursorIcon::NeResize,
                Side::NW => CursorIcon::NwResize,
                Side::SE => CursorIcon::SeResize,
                Side::SW => CursorIcon::SwResize,
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_corner_is_two_edges_and_gets_a_diagonal() {
        assert_eq!(Shape::for_edges(ResizeEdge::TOP | ResizeEdge::LEFT), Shape::Resize(Side::NW));
        assert_eq!(Shape::for_edges(ResizeEdge::BOTTOM | ResizeEdge::RIGHT), Shape::Resize(Side::SE));
        assert_eq!(Shape::for_edges(ResizeEdge::TOP | ResizeEdge::RIGHT), Shape::Resize(Side::NE));
        assert_eq!(Shape::for_edges(ResizeEdge::BOTTOM | ResizeEdge::LEFT), Shape::Resize(Side::SW));
    }

    #[test]
    fn a_single_edge_gets_a_single_axis() {
        assert_eq!(Shape::for_edges(ResizeEdge::TOP), Shape::Resize(Side::N));
        assert_eq!(Shape::for_edges(ResizeEdge::BOTTOM), Shape::Resize(Side::S));
        assert_eq!(Shape::for_edges(ResizeEdge::LEFT), Shape::Resize(Side::W));
        assert_eq!(Shape::for_edges(ResizeEdge::RIGHT), Shape::Resize(Side::E));
    }

    #[test]
    fn no_edges_is_not_a_resize() {
        // An empty set reaches here whenever a hit lands on a decoration that is not an edge, so
        // it must mean "ordinary pointer" rather than some arbitrary arrow.
        assert_eq!(Shape::for_edges(ResizeEdge::empty()), Shape::Default);
    }
}
