#include "teaching_tip.h"

#include <algorithm>
#include <vector>

namespace aurora::ui {
namespace {

float TextHeight(const Env& e, const TextStyle& st) {
    au_font_metrics m{};
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return m.ascent + m.descent + m.leading;
}

float LineBaseline(const Env& e, const TextStyle& st, float top, float h) {
    au_font_metrics m{};
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return top + (h + m.ascent - m.descent) * 0.5f;
}

// Break text into lines that fit `max_w`, honouring explicit newlines and splitting a single
// word that cannot fit at all. The last case matters for a tip: a URL or a path in the body of
// a first-run explanation would otherwise run past the panel, and a clipped instruction is
// worse than a hyphenless split.
std::vector<std::string> WrapText(au_font font, float size, const std::string& text, float max_w) {
    std::vector<std::string> lines;
    std::string line;
    auto fits = [&](const std::string& s) {
        return max_w <= 0 || au_text_width(font, size, s.c_str(), -1) <= max_w;
    };
    auto flush = [&]() {
        lines.push_back(line);
        line.clear();
    };

    size_t i = 0;
    while (i < text.size()) {
        const char c = text[i];
        if (c == '\n') { flush(); ++i; continue; }
        if (c == '\r' || c == ' ') { ++i; continue; }

        size_t j = i;
        while (j < text.size() && text[j] != ' ' && text[j] != '\n' && text[j] != '\r') ++j;
        std::string word = text.substr(i, j - i);
        i = j;

        std::string cand = line.empty() ? word : line + " " + word;
        if (fits(cand)) {
            line = std::move(cand);
            continue;
        }
        if (!line.empty()) {
            flush();
            cand = word;
        }
        // The word alone may still be too wide. Cut at a UTF-8 boundary so a multi-byte glyph
        // is never split in half; a single glyph wider than the line stays whole rather than
        // becoming an infinite loop.
        while (!fits(cand)) {
            size_t cut = cand.size() - 1;
            while (cut > 0 && ((unsigned char)cand[cut] & 0xC0) == 0x80) --cut;
            if (cut == 0) break;
            line = cand.substr(0, cut);
            flush();
            cand = cand.substr(cut);
        }
        line = std::move(cand);
    }
    if (!line.empty() || lines.empty()) flush();
    return lines;
}

class TeachingTipNode : public Node {
public:
    const char* Role() const override { return "note"; }

    TeachingTipNode(std::string title, std::string body, Binding<bool> show,
                    std::function<void()> on_dismiss, std::string dismiss_label)
        : title_(std::move(title)),
          body_(std::move(body)),
          show_(std::move(show)),
          on_dismiss_(std::move(on_dismiss)),
          dismiss_(Button(std::move(dismiss_label), [this] { Dismiss(); })) {
        // Binding, not a plain bool: a State cell must invalidate this node, and the change is a
        // LAYOUT change (zero height when hidden), which Animating() below reports.
        show_.attach(this);
        st_title_.bold = true;
        st_body_.size = Ratio(0.95f);   // one step down: the title leads, the body explains
    }

    Size Measure(const Env& e, Constraints c) override {
        applied_ = shown() ? 1 : 0;
        if (!shown()) return Size{0, 0};

        pad_ = e.unit * 1.5f;
        rail_ = e.unit * 0.375f;
        const float pref = e.unit * 34;               // a comfortable reading measure
        float avail = c.max_w >= 0 ? c.max_w : pref;
        if (avail <= 0) avail = pref;
        float w = std::min(pref, avail);

        // The dismiss button sets a floor on the width. A note that cannot be closed is a note
        // that stays on screen, and overflowing the offer is the lesser defect.
        btn_ = dismiss_->Measure(e, Constraints{w - pad_ * 2 - rail_, -1});
        const float floor_w = btn_.w + pad_ * 2 + rail_;
        if (floor_w > w) w = floor_w;

        const float inner_w = std::max(1.0f, w - pad_ * 2 - rail_);
        // Wrapping measures glyph advances, so it is cached per (text, width): a repaint of an
        // unchanged tip must not re-shape its body.
        if (cached_w_ != inner_w || cached_title_ != title_ || cached_body_ != body_) {
            cached_w_ = inner_w;
            cached_title_ = title_;
            cached_body_ = body_;
            title_lines_ = WrapText(e.face(st_title_), e.sized(st_title_), title_, inner_w);
            body_lines_ = WrapText(e.face(st_body_), e.sized(st_body_), body_, inner_w);
        }

        const float title_h = TextHeight(e, st_title_);
        const float body_h = TextHeight(e, st_body_);
        const float gap = e.unit * 0.5f;
        float h = pad_ * 2 + (float)title_lines_.size() * title_h;
        if (!body_.empty()) h += gap * 0.75f + (float)body_lines_.size() * body_h;
        h += gap + btn_.h;
        return Size{w, h};
    }

    void Place(Rect r) override {
        frame = r;
        if (!shown()) return;
        const Rect btn_rect{r.x + r.w - pad_ - btn_.w, r.y + r.h - pad_ - btn_.h, btn_.w, btn_.h};
        dismiss_->parent = this;
        dismiss_->Place(btn_rect);
    }

    void Draw(au_surface* s, const Env& e) override {
        if (!shown()) return;
        const float radius = std::min(7.0f, frame.h * 0.5f);
        au_fill_round_rect(s, frame.box(), radius, radius, e.surface_hi);
        au_stroke_round_rect(s, frame.box(), radius, radius, e.hairline, 1);

        // An accent rail instead of a coloured fill: the note is chrome, and the one colour the
        // eye should find is the thing it is about.
        const float rail_x = frame.x + pad_ * 0.5f;
        au_fill_round_rect(s,
                           au_rect{rail_x, frame.y + pad_ * 0.5f, rail_x + rail_ - 1,
                                   frame.y + frame.h - pad_ * 0.5f - 1},
                           rail_ * 0.5f, rail_ * 0.5f, e.accent);

        au_clip_set(s, frame.box());
        const float title_h = TextHeight(e, st_title_);
        const float gap = e.unit * 0.5f;
        float y = frame.y + pad_;
        const float x = frame.x + pad_ + rail_;
        for (const std::string& ln : title_lines_) {
            au_text_draw(s, e.face(st_title_), e.sized(st_title_), ln.c_str(), -1, x,
                         LineBaseline(e, st_title_, y, title_h), e.fg);
            y += title_h;
        }
        if (!body_.empty()) {
            const float body_h = TextHeight(e, st_body_);
            y += gap * 0.75f;
            for (const std::string& ln : body_lines_) {
                au_text_draw(s, e.face(st_body_), e.sized(st_body_), ln.c_str(), -1, x,
                             LineBaseline(e, st_body_, y, body_h), e.dim);
                y += body_h;
            }
        }
        au_clip_reset(s);

        dismiss_->Draw(s, e);
    }

    // The panel is opaque, so a click on it must not reach a row behind it; only the button acts.
    Node* HitTest(float x, float y) override {
        if (!shown()) return nullptr;
        if (Node* n = dismiss_->HitTest(x, y)) return n;
        return frame.has(x, y) ? this : nullptr;
    }

    // A pointing device cannot reach a note that has no button; Escape gives the keyboard the
    // same one action, and it arrives here by bubbling up from the focused dismiss button.
    bool OnKey(const au_key& k) override {
        if (!shown() || !k.pressed) return false;
        if (k.raw_char == 0x1b) {   // Escape
            Dismiss();
            return true;
        }
        return false;
    }

    void Children(std::vector<Node*>* out) override {
        if (shown()) out->push_back(dismiss_.get());
    }

    // Showing or hiding changes the node's size, so a state change needs a LAYOUT and not only a
    // repaint. The same contract Either answers with; `applied_` starts at -1 so the first pass
    // settles it without an extra layout.
    bool Animating() const override { return (int)(shown() ? 1 : 0) != applied_; }

    void SetStyle(const TextStyle& st) override { dismiss_->SetStyle(st); }

private:
    bool shown() const { return show_.get_or(false); }

    // The caller owns "seen"; this only reports the one action. Calling it twice is the caller's
    // callback seeing two dismissals, so the button is not hidden speculatively.
    void Dismiss() {
        if (on_dismiss_) on_dismiss_();
    }

    std::string title_, body_;
    Binding<bool> show_;
    std::function<void()> on_dismiss_;
    Desc dismiss_;

    TextStyle st_title_, st_body_;
    float pad_ = 0, rail_ = 0;
    Size btn_{};
    int applied_ = -1;

    std::vector<std::string> title_lines_, body_lines_;
    std::string cached_title_, cached_body_;
    float cached_w_ = -1;
};

}   // namespace

Desc TeachingTip(std::string title, std::string body, Binding<bool> show,
                 std::function<void()> on_dismiss, std::string dismiss_label) {
    return std::make_shared<TeachingTipNode>(std::move(title), std::move(body), std::move(show),
                                             std::move(on_dismiss), std::move(dismiss_label));
}

}   // namespace aurora::ui
