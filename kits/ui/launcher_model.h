// The launcher's inventory model: pure, no widgets, no bus. Given a `registry.apps@v1` reply it
// produces the sections the window draws. Kept apart from the app so the ordering and the
// greyed/un-greyed rules are unit-testable headless (the interrogate pass showed every bug in
// v1 would live here, not in the drawing).
//
// Rules, each one from a run-proven finding:
//  - Sections come from the controller's category vocabulary, in its order; EMPTY sections are
//    skipped (two of the five are empty in the current tree, and an empty "Files" header at the
//    top is the first thing a user would see).
//  - An app with no category (or a token the vocabulary does not know) goes to a trailing
//    "Other" section, so nothing silently disappears.
//  - A row is actionable only when the app is windowed AND launchable. `launchable` alone is not
//    enough: four verb-only apps report launchable=true and have no window to open.
//  - `windowed`/`launch_problem` are `omitempty` on the wire: absent means false / no reason.
#pragma once

#include <algorithm>
#include <string>
#include <vector>

namespace launcher {

struct Category {
    std::string id;
    std::string label;
};

struct App {
    std::string id;
    std::string name;
    std::string category;        // token; empty = none
    std::string launch_problem;  // only when launchable is false with a reason
    bool windowed = false;       // absent on the wire means false
    bool launchable = false;
};

struct Row {
    std::string id;      // the app id, for activation
    std::string label;   // the app name
    std::string note;    // right-aligned reason; empty = none
    bool enabled = true; // clickable / Return-activates
};

struct Section {
    std::string id;      // the category token (or "other"); row identity, not the label
    std::string label;
    std::vector<Row> rows;
};

inline Row row_for(const App& a) {
    Row r;
    r.id = a.id;
    r.label = a.name;
    r.enabled = a.windowed && a.launchable;
    if (!r.enabled) {
        // Prefer the controller's own reason; otherwise say why a row cannot be actioned.
        if (!a.launch_problem.empty()) r.note = a.launch_problem;
        else if (!a.windowed) r.note = "needs a file";
        else r.note = "cannot launch";   // defence: the controller always sends a reason for this case
    }
    return r;
}

inline void sort_by_name(std::vector<App>& v) {
    std::sort(v.begin(), v.end(), [](const App& a, const App& b) {
        return a.name == b.name ? a.id < b.id : a.name < b.name;   // total order on (name, id): repeatable, equal names fixed
    });
}

inline std::vector<Section> build_sections(const std::vector<App>& apps, const std::vector<Category>& cats) {
    std::vector<Section> out;
    std::vector<char> used(apps.size(), 0);
    for (const Category& c : cats) {
        std::vector<App> bucket;
        for (size_t i = 0; i < apps.size(); i++)
            if (!used[i] && apps[i].category == c.id) { bucket.push_back(apps[i]); used[i] = 1; }
        if (bucket.empty()) continue;                 // skip an empty section rather than draw a bare header
        sort_by_name(bucket);
        Section s;
        s.id = c.id;
        s.label = c.label;
        for (const App& a : bucket) s.rows.push_back(row_for(a));
        out.push_back(std::move(s));
    }
    std::vector<App> other;
    for (size_t i = 0; i < apps.size(); i++)
        if (!used[i]) other.push_back(apps[i]);       // no category, or a token not in the vocabulary
    if (!other.empty()) {
        sort_by_name(other);
        Section s;
        s.id = "other";
        s.label = "Other";
        for (const App& a : other) s.rows.push_back(row_for(a));
        out.push_back(std::move(s));
    }
    return out;
}

}  // namespace launcher
