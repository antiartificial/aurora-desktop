// The launcher model's rules, each pinned by a fixture shaped like the REAL registry.apps@v1 reply
// (probed live during the interrogate pass): empty sections, omitempty fields, verb-only apps that
// still report launchable=true, a launch_problem, and an app with no category.
#include "../demo/launcher_model.h"

#include <cstdio>

using namespace launcher;

static int fails = 0;
static void check(bool ok, const char* what) {
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    // The vocabulary, in order. "files" and "utilities" have no apps (as in the real tree today).
    std::vector<Category> cats = {
        {"files", "Files"}, {"media", "Media"}, {"development", "Development"},
        {"utilities", "Utilities"}, {"haiku", "Haiku"},
    };

    std::vector<App> apps;
    // windowed + launchable: actionable
    { App a; a.id = "org.aurora.haiku.text"; a.name = "Haiku Text"; a.category = "haiku"; a.windowed = true; a.launchable = true; apps.push_back(a); }
    // verb-only but launchable=true (proven: build/ffmpeg/lumen/magick): NOT actionable
    { App a; a.id = "org.aurora.ffmpeg"; a.name = "FFmpeg"; a.category = "media"; a.windowed = false; a.launchable = true; a.verbs = {"extract_audio@v1"}; apps.push_back(a); }
    // windowed but a launch_problem (a failed launch): greyed with the controller's reason
    { App a; a.id = "org.aurora.haiku.menu"; a.name = "Haiku Menu"; a.category = "haiku"; a.windowed = true; a.launchable = false; a.launch_problem = "last launch failed: nope"; apps.push_back(a); }
    // no category at all (omitempty): must land in Other, not vanish
    { App a; a.id = "org.test.nocat"; a.name = "No Category"; a.windowed = true; a.launchable = true; apps.push_back(a); }
    // media second app, to prove name ordering within a section
    { App a; a.id = "org.aurora.lumen"; a.name = "Lumen"; a.category = "media"; a.windowed = true; a.launchable = true; apps.push_back(a); }

    std::vector<Section> s = build_sections(apps, cats);

    // 1. Empty sections are skipped: only Media, Haiku and Other remain, in vocabulary order.
    check(s.size() == 3, "three non-empty sections");
    check(s.size() == 3 && s[0].label == "Media", "Media first (files skipped, media first with apps)");
    check(s.size() == 3 && s[1].label == "Haiku", "Haiku second");
    check(s.size() == 3 && s[2].label == "Other", "Other last");

    // 2. Within Media, name order: FFmpeg before Lumen.
    check(s.size() == 3 && s[0].rows.size() == 2 && s[0].rows[0].label == "FFmpeg" && s[0].rows[1].label == "Lumen", "rows sorted by name");

    // 3. Verb-only launchable=true is NOT actionable and says why.
    check(s[0].rows[0].enabled == false, "verb-only app is not actionable");
    check(s[0].rows[0].note == "needs a file", "verb-only app says needs a file");
    check(s[0].rows[1].enabled == true, "windowed+launchable app is actionable");
    check(s[0].rows[1].note.empty(), "actionable app has no note");

    // 4. A windowed app with a launch_problem is greyed WITH the controller's reason.
    check(s[1].rows.size() == 2, "haiku has two rows");
    // Haiku Menu (H) sorts before Haiku Text (H...>M<T): check by id, not position.
    const Row* menu = nullptr;
    for (const Row& r : s[1].rows) if (r.id == "org.aurora.haiku.menu") menu = &r;
    check(menu && !menu->enabled && menu->note == "last launch failed: nope", "launch_problem greys with the reason");
    const Row* text = nullptr;
    for (const Row& r : s[1].rows) if (r.id == "org.aurora.haiku.text") text = &r;
    check(text && text->enabled && text->note.empty(), "a healthy windowed app is actionable");

    // 5. An app with no category lands in Other, still actionable.
    check(s[2].rows.size() == 1 && s[2].rows[0].id == "org.test.nocat" && s[2].rows[0].enabled, "no-category app in Other, actionable");

    // 6. An all-empty inventory yields no sections (the window shows "no apps", not bare headers).
    std::vector<App> none;
    check(build_sections(none, cats).empty(), "no apps -> no sections");

    std::printf(fails ? "launcher_model: %d FAILED\n" : "launcher_model: ok (%d checks)\n", fails ? fails : 15);
    return fails ? 1 : 0;
}
