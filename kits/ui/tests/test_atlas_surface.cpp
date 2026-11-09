// P6 #5: Atlas publishes its own surface, so the shell header stops being a fixture.
//
// The check is deliberately about the BYTES the app writes, not about a copy of its declaration
// written again here: the names are read out of atlas.cpp (the file that owns them), turned into
// the same act: tree the toolkit walks, and encoded with the same SurfaceJson the app calls. When
// the binary is reachable the test also runs it with AURORA_SURFACE_PATH set and compares what it
// wrote byte for byte, which is the only evidence that the published document and the descriptor
// under test are one thing.
//
// The descriptor is location-independent by construction (app, title, actions, grants), so the
// binary is run twice in two different directories and the two files must be identical; that is
// the "nothing here depends on where you are" claim, checked rather than asserted in a comment.
#include "surface.h"

#include <sys/stat.h>
#include <sys/wait.h>
#include <unistd.h>

#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); \
                          printf(__VA_ARGS__); printf("\n"); } } while (0)

static std::string read_file(const std::string& p) {
    FILE* f = fopen(p.c_str(), "rb");
    if (!f) return std::string();
    std::string out;
    char buf[4096];
    size_t n;
    while ((n = fread(buf, 1, sizeof buf, f)) > 0) out.append(buf, n);
    fclose(f);
    return out;
}

static bool is_file(const std::string& p) {
    struct stat st;
    return stat(p.c_str(), &st) == 0 && S_ISREG(st.st_mode);
}

// atlas.cpp is the authority for the names, so the test searches the usual working directories
// rather than hard-coding one build layout: ctest runs from build/ui, a manual run from kits/ui,
// and a developer may run it from the repository root.
static std::string find_source() {
    const std::string self = __FILE__;
    const size_t slash = self.find_last_of('/');
    const std::string beside = slash == std::string::npos ? std::string()
                                                          : self.substr(0, slash) + "/../demo/atlas.cpp";
    const std::string candidates[] = {
        "demo/atlas.cpp", "ui/demo/atlas.cpp", "../ui/demo/atlas.cpp",
        "../../ui/demo/atlas.cpp", "kits/ui/demo/atlas.cpp", beside,
    };
    for (const std::string& c : candidates)
        if (is_file(c)) return c;
    return std::string();
}

// The action names, taken only from name("act:...") sites: the file's comments discuss "act:"
// freely, and a scan that matched those would be reading prose as a declaration.
static std::vector<std::string> action_names(const std::string& src) {
    std::vector<std::string> out;
    const std::string key = "name(\"act:";
    for (size_t i = 0; (i = src.find(key, i)) != std::string::npos; i += key.size()) {
        const size_t start = i + std::strlen("name(\"");
        const size_t end = src.find('"', start);
        if (end == std::string::npos) break;
        out.push_back(src.substr(start, end - start));
        i = end;
    }
    return out;
}

static bool find_action(const SurfaceDescriptor& d, const std::string& id, Action* out) {
    for (const Action& a : d.actions)
        if (a.id == id) { *out = a; return true; }
    return false;
}

// Run the real binary with the descriptor path set, and wait only for the FILE: the app publishes
// before it opens a window, so a compositor-less run still leaves the claim behind, and a run with
// a compositor is told to stop after one frame rather than waited on.
static bool run_atlas(const std::string& bin, const std::string& dir, const std::string& out) {
    unlink(out.c_str());
    const pid_t pid = fork();
    if (pid < 0) return false;
    if (pid == 0) {
        // The child has no compositor and no bus on purpose; its complaints are not this test's
        // output, and a shell that greps for FAIL must not see them.
        freopen("/dev/null", "w", stderr);
        setenv("AURORA_SURFACE_PATH", out.c_str(), 1);
        setenv("ATLAS_DIR", dir.c_str(), 1);
        setenv("ATLAS_FRAMES", "1", 1);
        setenv("ATLAS_SIZE", "480x320", 1);
        // Deterministic failure of the window path: this test is about the document, not a frame.
        unsetenv("WAYLAND_DISPLAY");
        unsetenv("DISPLAY");
        execl(bin.c_str(), bin.c_str(), static_cast<char*>(nullptr));
        _exit(127);
    }
    bool have = false;
    for (int i = 0; i < 500 && !have; i++) {          // up to 5 s
        have = is_file(out) && read_file(out).size() > 0;
        if (!have) usleep(10000);
    }
    int status = 0;
    if (!have) {
        kill(pid, SIGKILL);
        waitpid(pid, &status, 0);
        return false;
    }
    for (int i = 0; i < 100; i++) {                   // give a headless run time to exit by itself
        if (waitpid(pid, &status, WNOHANG) == pid) return true;
        usleep(10000);
    }
    kill(pid, SIGKILL);
    waitpid(pid, &status, 0);
    return true;
}

static std::string find_binary(int argc, char** argv) {
    if (argc > 1 && access(argv[1], X_OK) == 0) return argv[1];
    const char* candidates[] = {"atlas", "./atlas", "../atlas", "../../ui/atlas",
                                "build/ui/atlas", "../build/ui/atlas"};
    for (const char* c : candidates)
        if (access(c, X_OK) == 0) return c;
    return std::string();
}

int main(int argc, char** argv) {
    const std::string src_path = find_source();
    if (src_path.empty()) {
        printf("FAIL: cannot find demo/atlas.cpp from this working directory\n");
        return 1;
    }
    const std::string src = read_file(src_path);
    CHECK(!src.empty(), "atlas.cpp is readable");
    if (src.empty()) return 1;

    const std::vector<std::string> names = action_names(src);
    CHECK(names.size() == 5, "atlas declares five action nodes, found %zu", names.size());

    // The declaration, plus a node named without act: -- the exclusion is part of the rule, not an
    // accident of the scan, so the tree under test contains one.
    std::vector<Desc> nodes;
    for (const std::string& n : names) nodes.push_back(Label("").name(n));
    nodes.push_back(Label("").name("files"));
    Desc tree = VStack(std::move(nodes));

    SurfaceDescriptor d;
    std::string why;
    CHECK(SurfaceOf(tree, "org.example.atlas", "Atlas", {}, &d, &why),
          "Atlas's action nodes describe a surface: %s", why.c_str());
    CHECK(SurfaceOk(d, &why), "the descriptor validates: %s", why.c_str());
    CHECK(d.app == "org.example.atlas" && d.title == "Atlas", "app and title are Atlas's own");
    CHECK(d.grants.empty(), "Atlas claims no grants it cannot police");
    CHECK(d.actions.size() == names.size(), "every act: node became an action, got %zu",
          d.actions.size());

    // The verbs, with the effect Atlas's own menu assigns them.
    struct Want { const char* id; const char* label; const char* effect; };
    const Want want[] = {
        {"open", "Open", "read"},
        {"copy-path", "Copy path", "read"},
        {"rename", "Rename", "write"},
        {"new-folder", "New folder", "write"},
        {"move-to-trash", "Move to trash", "write"},
    };
    for (const Want& w : want) {
        Action a;
        const bool found = find_action(d, w.id, &a);
        CHECK(found, "the published surface offers \"%s\"", w.id);
        if (found)
            CHECK(a.label == w.label && a.effect == w.effect,
                  "\"%s\" is %s/%s, got %s/%s", w.id, w.label, w.effect, a.label.c_str(),
                  a.effect.c_str());
    }
    Action nav;
    CHECK(!find_action(d, "back", &nav) && !find_action(d, "forward", &nav) &&
              !find_action(d, "up", &nav),
          "navigation is not published as a verb");
    Action f;
    CHECK(!find_action(d, "files", &f), "a node named without act: is not an action");

    // Canonical bytes: sorted by id, one effect each, no grants key (empty is omitted). This is
    // the document the shell and the controller compare against, so it is asserted exactly.
    const std::string expected =
        R"({"app":"org.example.atlas","title":"Atlas","actions":[)"
        R"({"id":"copy-path","label":"Copy path","effect":"read"},)"
        R"({"id":"move-to-trash","label":"Move to trash","effect":"write"},)"
        R"({"id":"new-folder","label":"New folder","effect":"write"},)"
        R"({"id":"open","label":"Open","effect":"read"},)"
        R"({"id":"rename","label":"Rename","effect":"write"}]})";
    std::string bytes;
    CHECK(SurfaceJson(d, &bytes, &why), "the descriptor encodes: %s", why.c_str());
    CHECK(bytes == expected, "the canonical bytes are the contract's shape");
    if (bytes != expected) printf("      got: %s\n     want: %s\n", bytes.c_str(), expected.c_str());

    // A tree with no act: nodes has no actions at all -- the exclusion, stated on its own.
    SurfaceDescriptor empty;
    CHECK(SurfaceOf(Label("files").name("files"), "org.example.atlas", "Atlas", {}, &empty, &why),
          "a tree of plain names still describes");
    CHECK(empty.actions.empty(), "a tree of plain names offers no actions");

    // ---- the bytes the binary actually writes ------------------------------------------------
    const std::string bin = find_binary(argc, argv);
    if (bin.empty()) {
        printf("  (atlas binary not found: the publish check is skipped)\n");
    } else {
        char tmpl[] = "/tmp/atlassurface.XXXXXX";
        const char* td = mkdtemp(tmpl);
        if (!td) {
            printf("FAIL: no temp directory for the publish check\n");
            ++fails;
        } else {
            const std::string dir = td;
            const std::string sub = dir + "/sub";
            mkdir(sub.c_str(), 0755);
            const std::string out_a = dir + "/surface.json", out_b = dir + "/surface-b.json";
            const bool ran_a = run_atlas(bin, dir, out_a);
            const bool ran_b = run_atlas(bin, sub, out_b);
            CHECK(ran_a, "%s published AURORA_SURFACE_PATH", bin.c_str());
            if (ran_a) {
                const std::string got = read_file(out_a);
                CHECK(got == expected, "the published bytes are exactly the canonical descriptor");
                if (got != expected) printf("      got: %s\n", got.c_str());
            }
            // The same descriptor in a different directory: nothing in the document depends on
            // where Atlas is looking, which is why navigation does not republish it.
            if (ran_a && ran_b) {
                CHECK(read_file(out_a) == read_file(out_b),
                      "the descriptor is identical from two different directories");
                struct stat st;
                CHECK(stat(out_a.c_str(), &st) == 0 && st.st_size == (off_t)expected.size(),
                      "the descriptor file is exactly %zu bytes", expected.size());
            }
            unlink(out_a.c_str());
            unlink(out_b.c_str());
            rmdir(sub.c_str());
            rmdir(dir.c_str());
        }
    }

    printf("test_atlas_surface: %d checks, %d failures\n", checks, fails);
    return fails ? 1 : 0;
}
