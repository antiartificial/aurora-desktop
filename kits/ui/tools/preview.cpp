// aurora-preview: P3's loop. Every registered state, rendered at every viewport, diffed against
// goldens. ui_specimen is four fixed sheets that assert nothing; this is the other thing -- a
// registry, a viewport loop and a differ, so a one-pixel regression fails a command instead of
// waiting for someone to notice a picture in a document.
//
//   aurora-preview --list
//   aurora-preview --states DIR     write DIR/<state>-<WxH>.{png,argb}
//   aurora-preview --check DIR      render and diff against DIR; exit 1 on any difference
//   aurora-preview --watch DIR      re-check whenever this binary is rebuilt
//
// Goldens are compared as pixels (the .argb sidecar), not as PNG bytes, so a failure can say where
// the first difference is. The PNG is for a person to look at.
#include "ui.h"

#include <zlib.h>

#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include <sys/stat.h>

using namespace aurora::ui;

// ---- PNG, with no image library ---------------------------------------------------------------
static void be32(std::string& out, uint32_t v) {
    const uint8_t b[4] = {(uint8_t)(v >> 24), (uint8_t)(v >> 16), (uint8_t)(v >> 8), (uint8_t)v};
    out.append((const char*)b, 4);
}
static void chunk(std::string& out, const char* tag, const std::string& data) {
    be32(out, (uint32_t)data.size());
    std::string body(tag);
    body += data;
    out += body;
    be32(out, (uint32_t)crc32(0, (const Bytef*)body.data(), (uInt)body.size()));
}

static bool write_png(const std::string& path, const uint32_t* argb, int w, int h) {
    std::string raw;
    raw.reserve((size_t)h * (1 + (size_t)w * 3));
    for (int y = 0; y < h; y++) {
        raw.push_back('\0');   // filter 0
        for (int x = 0; x < w; x++) {
            const uint32_t p = argb[(size_t)y * w + x];
            raw.push_back((char)((p >> 16) & 0xff));
            raw.push_back((char)((p >> 8) & 0xff));
            raw.push_back((char)(p & 0xff));
        }
    }
    uLongf bound = compressBound((uLong)raw.size());
    std::string z(bound, '\0');
    if (compress2((Bytef*)z.data(), &bound, (const Bytef*)raw.data(), (uLong)raw.size(), 6) != Z_OK)
        return false;
    z.resize(bound);

    std::string png("\x89PNG\r\n\x1a\n", 8);
    std::string ihdr;
    be32(ihdr, (uint32_t)w);
    be32(ihdr, (uint32_t)h);
    ihdr.push_back(8);    // bit depth
    ihdr.push_back(2);    // truecolour
    ihdr.push_back(0);
    ihdr.push_back(0);
    ihdr.push_back(0);
    chunk(png, "IHDR", ihdr);
    chunk(png, "IDAT", z);
    chunk(png, "IEND", std::string());

    FILE* f = fopen(path.c_str(), "wb");
    if (!f) return false;
    const bool ok = fwrite(png.data(), 1, png.size(), f) == png.size();
    fclose(f);
    return ok;
}

// ---- rendering, the way a window does it ------------------------------------------------------
static Env g_env;

static au_surface* render(const Desc& d, float w, float h) {
    au_surface* s = au_surface_new((int)ceilf(w), (int)ceilf(h), 1.0);
    au_clear(s, g_env.content);
    d->Measure(g_env, Constraints{w, h});
    d->Place(Rect{0, 0, w, h});
    d->Draw(s, g_env);
    return s;
}

// ---- the state registry: the cases nobody remembers to check -----------------------------------
static std::string repeat_join(const char* unit, int n) {
    std::string s;
    for (int i = 0; i < n; i++) { if (i) s += " "; s += unit; }
    return s;
}

static Desc st_empty() {
    return VStack({Bold("Nothing here"), Label("This folder is empty.")}, 0.5f).padding(1.5f);
}

static Desc st_permission_denied() {
    return VStack({Bold("Permission denied"),
                   Label("You do not have access to this folder. Ask its owner for read access."),
                   Button("Try again", [] {})},
                  0.5f).padding(1.5f);
}

static Desc st_long_name() {
    const std::string n = repeat_join("rechnung_2024_quartal_wirtschaftspruefung", 5);
    return VStack({Bold("220-character name"),
                   Label(n),
                   Label("The name wraps rather than clipping, and the row grows.")},
                  0.5f).padding(1.5f);
}

static Desc st_cjk() {
    return VStack({Bold("CJK"), Label("文件管理器 — 日本語のフォルダ名 — 한글 폴더"),
                   Label("Mixed: Report 2024 — 報告書.pdf")},
                  0.5f).padding(1.5f);
}

static Desc st_rtl() {
    return VStack({Bold("RTL"), Label("مرحبا بالعالم — مجلد المستندات"),
                   Label("Mixed: Documents — مستندات.pdf")},
                  0.5f).padding(1.5f);
}

static Desc st_broken_link() {
    return VStack({Bold("Broken link"), Label("shortcut.desktop → /media/usb/report.pdf"),
                   Label("The target was moved or removed."), Button("Locate…", [] {})},
                  0.5f).padding(1.5f);
}

static Desc st_shell() {
    Desc sidebar = VStack({Bold("Places"), Label("Home"), Label("Documents"), Label("Downloads"),
                           Label("Trash")},
                          0.25f).padding(1.0f).weight(0.3f);
    Desc list = VStack({Bold("Documents"), Label("Report.pdf"), Label("Notes.md"),
                        Label("photo.png"), Label("budget.xlsx")},
                       0.25f).padding(1.0f).weight(1.0f);
    return HStack({sidebar, list}, 0.0f);
}

static Desc st_controls() {
    return VStack({Bold("Controls"), Button("Open", [] {}), Button("Disabled", [] {}),
                   Label("A button row, a long label, and a second line that wraps when the "
                         "viewport is narrow so the 360-pixel run is not just empty space.")},
                  0.5f).padding(1.5f);
}

struct PreviewState { const char* name; Desc (*build)(); };
static const PreviewState kStates[] = {
    {"empty", st_empty},         {"permission-denied", st_permission_denied},
    {"long-name", st_long_name}, {"cjk", st_cjk},
    {"rtl", st_rtl},             {"broken-link", st_broken_link},
    {"shell", st_shell},         {"controls", st_controls},
};

struct Viewport { int w, h; };
static const Viewport kViewports[] = {{360, 640}, {768, 1024}, {1440, 900}};

// ---- goldens ----------------------------------------------------------------------------------
static std::string stem(const char* state, const Viewport& v) {
    char buf[160];
    snprintf(buf, sizeof buf, "%s-%dx%d", state, v.w, v.h);
    return buf;
}

static bool save_golden(const std::string& dir, const std::string& base, au_surface* s) {
    return write_png(dir + "/" + base + ".png", au_surface_pixels(s), au_surface_px_w(s),
                     au_surface_px_h(s));
}

// Our own encoder writes filter 0 only, so the decoder needs exactly that: inflate, then strip the
// one filter byte per row. A golden edited by some other tool may use another filter; we refuse it
// loudly rather than decode it wrong.
struct Golden { int w = 0, h = 0; std::vector<uint32_t> px; };

static uint32_t rd32(const unsigned char* p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | p[3];
}

static bool load_golden(const std::string& path, Golden* g) {
    FILE* f = fopen(path.c_str(), "rb");
    if (!f) return false;
    std::vector<unsigned char> buf;
    unsigned char tmp[65536];
    size_t n;
    while ((n = fread(tmp, 1, sizeof tmp, f)) > 0) buf.insert(buf.end(), tmp, tmp + n);
    fclose(f);
    if (buf.size() < 8 || memcmp(buf.data(), "\x89PNG\r\n\x1a\n", 8) != 0) return false;

    std::string idat;
    int bitdepth = 0, colortype = -1;
    for (size_t off = 8; off + 8 <= buf.size();) {
        const uint32_t len = rd32(&buf[off]);
        const std::string tag((const char*)&buf[off + 4], 4);
        if (off + 12 + len > buf.size()) return false;
        const unsigned char* body = &buf[off + 8];
        if (tag == "IHDR" && len >= 13) {
            g->w = (int)rd32(body);
            g->h = (int)rd32(body + 4);
            bitdepth = body[8];
            colortype = body[9];
        } else if (tag == "IDAT") {
            idat.append((const char*)body, len);
        } else if (tag == "IEND") {
            break;
        }
        off += 12 + len;
    }
    if (bitdepth != 8 || colortype != 2 || g->w <= 0 || g->h <= 0) return false;

    const size_t stride = (size_t)g->w * 3;
    std::vector<unsigned char> raw((stride + 1) * (size_t)g->h);
    uLongf out = (uLongf)raw.size();
    if (uncompress((Bytef*)raw.data(), &out, (const Bytef*)idat.data(), (uLong)idat.size()) != Z_OK)
        return false;
    if (out != raw.size()) return false;

    g->px.resize((size_t)g->w * (size_t)g->h);
    for (int y = 0; y < g->h; y++) {
        const unsigned char* row = &raw[(stride + 1) * (size_t)y];
        if (row[0] != 0) return false;   // a filter we do not decode
        for (int x = 0; x < g->w; x++) {
            const unsigned char* p = row + 1 + (size_t)x * 3;
            g->px[(size_t)y * (size_t)g->w + x] =
                0xff000000u | ((uint32_t)p[0] << 16) | ((uint32_t)p[1] << 8) | p[2];
        }
    }
    return true;
}

int main(int argc, char** argv) {
    std::string mode, dir;
    for (int i = 1; i < argc; i++) {
        const std::string a = argv[i];
        if (a == "--list") mode = "list";
        else if (a == "--states" && i + 1 < argc) { mode = "states"; dir = argv[++i]; }
        else if (a == "--check" && i + 1 < argc) { mode = "check"; dir = argv[++i]; }
        else if (a == "--watch" && i + 1 < argc) { mode = "watch"; dir = argv[++i]; }
    }
    if (mode.empty()) {
        fprintf(stderr,
                "usage: aurora-preview --list | --states DIR | --check DIR | --watch DIR\n");
        return 2;
    }

    float sys = 0;
    if (!au_font_system("plain", &g_env.font, &sys)) {
        fprintf(stderr, "aurora-preview: no system font; nothing drawn\n");
        return 1;
    }
    if (!au_font_system("bold", &g_env.bold, &sys)) g_env.bold = g_env.font;
    g_env.size = Env::kBaseSize;

    if (mode == "list") {
        for (const PreviewState& st : kStates) printf("%-18s", st.name);
        printf("\nviewports:");
        for (const Viewport& v : kViewports) printf(" %dx%d", v.w, v.h);
        printf("\n");
        return 0;
    }

    const int total = (int)(sizeof kStates / sizeof *kStates) *
                      (int)(sizeof kViewports / sizeof *kViewports);
    auto run_check = [&]() -> int {
        int files = 0, bad = 0;
        for (const PreviewState& st : kStates) {
            for (const Viewport& v : kViewports) {
                au_surface* s = render(st.build(), (float)v.w, (float)v.h);
                Golden g;
                const std::string base = stem(st.name, v);
                if (!load_golden(dir + "/" + base + ".png", &g)) {
                    printf("MISSING %s\n", base.c_str());
                    ++bad;
                } else {
                    ++files;
                    const int w = au_surface_px_w(s), h = au_surface_px_h(s);
                    const uint32_t* px = au_surface_pixels(s);
                    if (w != g.w || h != g.h) {
                        printf("FAIL %s: size %dx%d, golden %dx%d\n", base.c_str(), w, h, g.w, g.h);
                        ++bad;
                    } else {
                        size_t n = 0;
                        int fx = -1, fy = -1;
                        for (int y = 0; y < h; y++)
                            for (int x = 0; x < w; x++)
                                if (px[(size_t)y * w + x] != g.px[(size_t)y * w + x]) {
                                    if (fx < 0) { fx = x; fy = y; }
                                    ++n;
                                }
                        if (n) {
                            printf("FAIL %s: %zu px differ, first at (%d,%d)\n", base.c_str(), n, fx,
                                   fy);
                            ++bad;
                        }
                    }
                }
                au_surface_free(s);
            }
        }
        if (bad) printf("preview: %d of %d images differ\n", bad, total);
        else printf("preview: %d images, all match\n", files);
        return bad ? 1 : 0;
    };

    if (mode == "states") {
        int n = 0;
        for (const PreviewState& st : kStates)
            for (const Viewport& v : kViewports) {
                au_surface* s = render(st.build(), (float)v.w, (float)v.h);
                if (!save_golden(dir, stem(st.name, v), s)) {
                    fprintf(stderr, "aurora-preview: cannot write %s/%s\n", dir.c_str(),
                            stem(st.name, v).c_str());
                    au_surface_free(s);
                    return 1;
                }
                au_surface_free(s);
                ++n;
            }
        printf("preview: wrote %d images to %s\n", n, dir.c_str());
        return 0;
    }

    if (mode == "check") return run_check();

    // watch: re-check whenever the binary is rebuilt (an external build is the edit signal, so the
    // loop never guesses which sources matter).
    auto mtime = [argv] {
        struct stat st{};
        return stat(argv[0], &st) == 0 ? (long long)st.st_mtime : -1LL;
    };
    long long last = mtime();
    int rc = run_check();
    printf("preview: watching (rebuild to re-check)\n");
    for (;;) {
        std::this_thread::sleep_for(std::chrono::milliseconds(400));
        const long long now = mtime();
        if (now != last) {
            last = now;
            printf("--- rebuild detected ---\n");
            rc = run_check();
        }
        (void)rc;
    }
}
