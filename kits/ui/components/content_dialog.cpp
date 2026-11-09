#include "content_dialog.h"

#include <algorithm>
#include <atomic>
#include <memory>
#include <utility>

namespace aurora::ui {

const char* AffirmLabel(Affirm a) {
    switch (a) {
        case Affirm::Accept: return "OK";
        case Affirm::Apply: return "Apply";
        case Affirm::Save: return "Save";
        case Affirm::Open: return "Open";
        case Affirm::Continue: return "Continue";
        case Affirm::Retry: return "Retry";
    }
    return nullptr;
}

namespace {

// The plate: comfortably narrower than the window so the scrim reads as a scrim, and wide enough
// for two sentences without a ragged column. In layout units, so a theme's unit scales it.
constexpr float kMaxPlateUnits = 44;
constexpr float kPlateMarginUnits = 3;     // scrim between the plate and the window edge
constexpr float kPlatePaddingUnits = 2;

// The body. A Label cannot wrap and a TextView brings its own field fill and scrollbar, neither
// of which belongs on a dialog plate, so this is the wrapped paragraph a dialog actually needs:
// measure at the offered width, break at spaces, break a long token mid-word, draw the lines.
//
// It clips to its own frame. A body taller than the space left for it is then cut off at the
// plate rather than painted over the buttons below it, and the reason is a layout consequence,
// not a decision to hide text: the dialog was given a window too short for its question, and the
// plate shrinking is the honest picture of that. A scrolling body would be the fix and would also
// let a confirmation grow a scrollbar, which is why it is not one.
class BodyNode : public Node {
public:
    explicit BodyNode(std::string text) : text_(std::move(text)) {}

    const char* Role() const override { return "text"; }

    Size Measure(const Env& e, Constraints c) override {
        const float w = c.max_w >= 0 ? c.max_w : 40 * e.unit;
        wrap(e, w);
        float h = (float)lines_.size() * line_h(e);
        // The dialog caps this when the window is too short for the question; the lines beyond the
        // cap are clipped at Draw rather than drawn over the buttons below.
        if (c.max_h >= 0) h = std::min(h, c.max_h);
        return Size{w + pad.l + pad.r, h + pad.t + pad.b};
    }

    void Draw(au_surface* s, const Env& e) override {
        const Rect in = inner();
        const float lh = line_h(e);
        au_font_metrics m{};
        const float baseline = au_font_metrics_get(e.font, e.size, &m) ? m.ascent : e.size * 0.8f;
        au_clip_set(s, frame.box());
        for (size_t i = 0; i < lines_.size(); i++)
            au_text_draw(s, e.font, e.size, lines_[i].c_str(), -1, in.x,
                         in.y + (float)i * lh + baseline, e.fg);
        au_clip_reset(s);
    }

    // Text is not a pointer target, the same rule every label in the toolkit keeps.
    Node* HitTest(float, float) override { return nullptr; }

private:
    // A metrics miss (no font backend, as on a machine with no faces installed) must still lay
    // out, so the fallback is a ratio of the text size rather than zero.
    static float line_h(const Env& e) {
        au_font_metrics m{};
        if (!au_font_metrics_get(e.font, e.size, &m)) return e.size * 1.35f;
        return m.ascent + m.descent + m.leading;
    }

    void wrap(const Env& e, float width) {
        lines_.clear();
        if (text_.empty()) return;          // no body is no line, not one blank one
        size_t pos = 0;
        for (;;) {
            const size_t nl = text_.find('\n', pos);
            const std::string para = text_.substr(pos, nl == std::string::npos
                                                           ? std::string::npos
                                                           : nl - pos);
            wrap_paragraph(e, para, width);
            if (nl == std::string::npos) break;
            pos = nl + 1;
        }
    }

    void wrap_paragraph(const Env& e, const std::string& para, float width) {
        if (para.empty()) {
            lines_.push_back(std::string());      // a blank line is a paragraph break, not nothing
            return;
        }
        std::string cur;
        size_t i = 0;
        while (i < para.size()) {
            while (i < para.size() && para[i] == ' ') i++;
            size_t j = para.find(' ', i);
            if (j == std::string::npos) j = para.size();
            const std::string word = para.substr(i, j - i);
            i = j;
            if (word.empty()) continue;

            // A token wider than the plate is broken mid-word, as TextView does: a path or an id
            // must be readable rather than vanish off the edge.
            if (au_text_width(e.font, e.size, word.c_str(), -1) > width) {
                if (!cur.empty()) {
                    lines_.push_back(cur);
                    cur.clear();
                }
                size_t start = 0;
                while (start < word.size()) {
                    const size_t take = prefix_that_fits(e, word, start, width);
                    if (start + take >= word.size()) {
                        cur = word.substr(start);
                        break;
                    }
                    lines_.push_back(word.substr(start, take));
                    start += take;
                }
                continue;
            }

            const std::string cand = cur.empty() ? word : cur + " " + word;
            if (au_text_width(e.font, e.size, cand.c_str(), -1) <= width) {
                cur = cand;
            } else {
                lines_.push_back(cur);
                cur = word;
            }
        }
        if (!cur.empty()) lines_.push_back(cur);
    }

    // Binary search over the kept length, the same shape the list's elide uses: measuring every
    // prefix would be one shaping call per character of every long word.
    size_t prefix_that_fits(const Env& e, const std::string& s, size_t start, float width) {
        size_t lo = 1, hi = s.size() - start, best = 1;
        while (lo <= hi) {
            const size_t mid = (lo + hi) / 2;
            if (au_text_width(e.font, e.size, s.substr(start, mid).c_str(), (int)mid) <= width) {
                best = mid;
                lo = mid + 1;
            } else {
                if (mid == 1) break;
                hi = mid - 1;
            }
        }
        return best;
    }

    std::string text_;
    std::vector<std::string> lines_;
};

// The dialog itself: scrim, plate, and the three things a confirmation is made of.
//
// It is a NODE rather than a stack of widgets because the two properties that make a dialog a
// dialog are not expressible in a stack: it has to paint behind everything it was given (the
// scrim) and it has to consume a click that lands outside its plate. A stack draws nothing of its
// own and its hit test returns nullptr between children, so a click on the scrim would fall
// through to the list underneath -- which is the one thing a modal must not do.
class DialogNode : public Node {
public:
    DialogNode(std::string title, std::string body, Affirm primary,
               std::function<void()> on_confirm, std::function<void()> on_cancel)
        : on_confirm_(std::move(on_confirm)), on_cancel_(std::move(on_cancel)) {
        title_ = Bold(std::move(title)).style(TextStyle{Ratio(1.2f), true});
        body_ = std::make_shared<BodyNode>(std::move(body));
        Desc cancel_node = Button("Cancel", [this] { Cancel(); }).name(kDialogCancelName);
        Desc primary_node = Button(AffirmLabel(primary), [this] { Confirm(); })
                                .name(kDialogPrimaryName);
        // Cancel first, primary last: reading order, and the primary ends up on the right where
        // every platform in this family puts the default. The spacer is what keeps the pair on
        // the right edge when the plate is wider than the words.
        buttons_ = HStack({Spacer(), cancel_node, primary_node}, 0.5f);
    }

    const char* Role() const override { return "dialog"; }

    // The vertical arrangement is done here rather than by a VStack, and the reason is the height
    // cap: the body has to be told how much room is LEFT after the title and the buttons, and a
    // stack measures its children before it knows that. Left to a stack, a long body pushed the
    // buttons past the plate, where the plate's clip then hid them -- a confirmation with no
    // visible way to answer it. Counting the fixed pieces first makes that impossible.
    Size Measure(const Env& e, Constraints c) override {
        unit_ = e.unit;
        pad_ = kPlatePaddingUnits * e.unit;
        const float gap = e.unit;
        const float margin = kPlateMarginUnits * e.unit;

        const float room_w = c.max_w >= 0 ? std::max(0.0f, c.max_w - 2 * margin)
                                          : kMaxPlateUnits * e.unit;
        plate_w_ = std::min(kMaxPlateUnits * e.unit, room_w);
        const float inner_w = std::max(0.0f, plate_w_ - 2 * pad_);

        title_sz_ = title_->Measure(e, Constraints{inner_w, -1});
        buttons_sz_ = buttons_->Measure(e, Constraints{inner_w, -1});

        const float cap_h = c.max_h >= 0 ? std::max(0.0f, c.max_h - 2 * margin) : -1;
        const float fixed = title_sz_.h + buttons_sz_.h + 2 * gap + 2 * pad_;
        const float body_room = cap_h >= 0 ? std::max(0.0f, cap_h - fixed) : -1;
        body_sz_ = body_->Measure(e, Constraints{inner_w, body_room});

        plate_h_ = fixed + body_sz_.h;
        // Not capped to cap_h: a window too short for even the title and the buttons would
        // otherwise draw a plate whose answer buttons are outside it. The BODY gives way (it is
        // capped above and clipped in Draw), the buttons never do -- an unanswered confirmation is
        // a worse outcome than a plate that reaches past its margins.

        if (c.max_w < 0 || c.max_h < 0)      // no offer: state a natural size instead of guessing
            return Size{c.max_w >= 0 ? c.max_w : plate_w_ + 2 * margin,
                        c.max_h >= 0 ? c.max_h : plate_h_ + 2 * margin};
        return Size{c.max_w, c.max_h};
    }

    void Place(Rect r) override {
        frame = r;
        const float w = std::min(plate_w_, frame.w);
        const float h = std::min(plate_h_, frame.h);
        plate_ = Rect{frame.x + (frame.w - w) / 2, frame.y + (frame.h - h) / 2, w, h};

        const float inner_w = std::max(0.0f, plate_.w - 2 * pad_);
        const float gap = unit_;
        float y = plate_.y + pad_;
        title_->parent = this;
        title_->Place(Rect{plate_.x + pad_, y, inner_w, title_sz_.h});
        y += title_sz_.h + gap;
        body_->parent = this;
        body_->Place(Rect{plate_.x + pad_, y, inner_w, body_sz_.h});
        y += body_sz_.h + gap;
        buttons_->parent = this;
        buttons_->Place(Rect{plate_.x + pad_, y, inner_w, buttons_sz_.h});
    }

    void Draw(au_surface* s, const Env& e) override {
        // The scrim is the darkest named role taken down in alpha, not a fourth near-black literal:
        // the palette is data (Env) and a surface that dims the window is the window's own colour
        // dimmed, not a new invention.
        au_color scrim = e.field;
        scrim.a = 208;
        au_fill_rect(s, frame.box(), scrim);

        au_fill_round_rect(s, plate_.box(), 10, 10, e.surface_hi);
        au_stroke_round_rect(s, plate_.box(), 10, 10, e.hairline, 1);

        // Children are clipped to the plate. A long title, or a body the height cap cut short,
        // then stops at the plate instead of painting over the scrim or past the rounded corner.
        //
        // Re-armed after the body: au_clip_reset returns to the frame's BASE clip, not to the one
        // this node just set, so a child that clips itself (the wrapped body does) would otherwise
        // leave the plate's clip behind for whatever draws next.
        au_clip_set(s, plate_.box());
        title_->Draw(s, e);
        body_->Draw(s, e);
        au_clip_set(s, plate_.box());
        buttons_->Draw(s, e);
        au_clip_reset(s);
    }

    // Inside the plate a child answers first; anywhere else inside the scrim the DIALOG answers,
    // so the press is consumed and the pane behind it never sees a click. That is the whole
    // pointer half of modality.
    Node* HitTest(float x, float y) override {
        if (!frame.has(x, y)) return nullptr;
        if (plate_.has(x, y)) {
            if (Node* n = title_->HitTest(x, y)) return n;   // text: always nullptr
            if (Node* n = body_->HitTest(x, y)) return n;
            if (Node* n = buttons_->HitTest(x, y)) return n;
        }
        return this;
    }

    // Also modal against the wheel: a list under a confirmation must not scroll while the user
    // reads it. Swallowed rather than forwarded, because this node has nothing to scroll.
    bool OnWheel(float) override { return true; }

    // Keys that reach the dialog and not a focused button. The buttons answer Return themselves,
    // which is why confirm-on-Return lives here as a fallback and not as the only path.
    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        if (k.raw_char == 0x1b) {                       // Escape
            Cancel();
            return true;
        }
        if (k.raw_char == 0x0d || k.raw_char == 0x03 || // Return, keypad Enter
            k.utf8[0] == '\r' || k.utf8[0] == '\n') {
            Confirm();
            return true;
        }
        return false;
    }

    void Children(std::vector<Node*>* out) override {
        out->push_back(title_.get());
        out->push_back(body_.get());
        out->push_back(buttons_.get());
    }

    // The default button takes focus when the dialog is on screen. This is normally the wrong
    // thing to do with a confirmation -- a stray Return would then commit whatever was asked --
    // and it is right here for the reason the whole component exists: the primary cannot be
    // destructive, so Return can only do the thing the person already asked for.
    //
    // Re-asked whenever focus is NOT inside this dialog, not just on the first layout: Either
    // keeps both branches alive, so a dialog that is closed and opened again is the same node, and
    // the Window drops a focus that is no longer in its focusable list. Without this a re-opened
    // dialog came back with no keyboard focus and a keyboard user had to Tab into it.
    //
    // Deferred, because Attached runs inside the window's focus collection: focusing by name while
    // that list is being built would address a half-built window. `asked_` keeps a failed lookup
    // from queueing one attempt per layout. The lifetime token guards a window that closes before
    // the queue is drained; no `this` is captured, so a dialog swapped out first just fails the
    // lookup.
    void Attached(Window* w) override {
        if (!w) return;
        for (Node* n = w->focus(); n; n = n->parent) {
            if (n == this) {
                asked_ = false;         // focus is here; a later appearance asks again
                return;
            }
        }
        if (asked_) return;
        asked_ = true;
        std::shared_ptr<std::atomic<bool>> life = w->lifetimeToken();
        w->Defer([w, life] {
            if (life->load(std::memory_order_acquire)) w->FocusNamed(kDialogPrimaryName);
        });
    }

private:
    void Confirm() { if (on_confirm_) on_confirm_(); }
    void Cancel() { if (on_cancel_) on_cancel_(); }

    Desc title_;
    std::shared_ptr<BodyNode> body_;
    Desc buttons_;
    Rect plate_;
    Size title_sz_, body_sz_, buttons_sz_;
    float plate_w_ = 0, plate_h_ = 0;
    float pad_ = 16, unit_ = 8;      // remembered from Measure: Place gets no Env
    std::function<void()> on_confirm_, on_cancel_;
    bool asked_ = false;             // a focus request is queued and has not landed yet
};

}   // namespace

Desc ContentDialog(std::string title, std::string body, Affirm primary,
                   std::function<void()> on_confirm, std::function<void()> on_cancel) {
    // A value outside the enum is the one way a destructive verb could still arrive, and a cast is
    // not a label table. Refuse rather than draw a null caption: a button with no word on it is
    // worse than a dialog that did not open. Affirm::Accept is the safe substitution.
    if (AffirmLabel(primary) == nullptr) primary = Affirm::Accept;
    return std::make_shared<DialogNode>(std::move(title), std::move(body), primary,
                                        std::move(on_confirm), std::move(on_cancel));
}

}   // namespace aurora::ui
