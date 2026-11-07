package launcher

// `aurora ui`: read an application's accessibility tree (decision 0017).
//
//	aurora ui tree APP [--window N] [--root TOKEN] [--depth N] [--no-text] [--json]
//	aurora ui node APP WINDOW TOKEN [--json]
//	aurora ui text APP WINDOW TOKEN [--deep] [--json]
//
// APP is the bus service the application registered when it was launched with AURORA_A11Y_SERVICE: "ui.kitchensink",
// or just "kitchensink" (the "ui." is added). Nothing here reads a window by itself: the application's own app_server
// builds the tree and answers, and decides from the identity busd stamped on the request whether the caller may have it.
// This command sends no identity and applies no filter; a refusal is printed as the service's word, and exits 1.
//
// Every string in a reply is text some application drew; it reaches the terminal through short()/clean() like any
// other untrusted text here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

var uiServiceRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// UIBackend is the optional half of a Backend that can ask an application for its tree.
type UIBackend interface {
	// UICall sends one request to the service and returns its JSON reply and the sender busd stamped on it.
	UICall(ctx context.Context, service, verb string, req any) (json.RawMessage, string, error)
}

// UIError is a refusal or failure from the service, with its code.
type UIError struct{ Service, Verb, Code, Message string }

func (e *UIError) Error() string {
	return fmt.Sprintf("%s %s: %s: %s", e.Service, e.Verb, e.Code, e.Message)
}

func (b *busBackend) UICall(ctx context.Context, service, verb string, req any) (json.RawMessage, string, error) {
	c, err := b.conn()
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	r, err := c.Call(ctx, proto.Envelope{To: service, Verb: verb, Payload: payload})
	if err != nil {
		var re *client.RemoteError
		if errors.As(err, &re) {
			if re.Code == "no_such_service" {
				return nil, "", fmt.Errorf("no application is serving a tree as %q: it must be launched with AURORA_A11Y_SERVICE=%s", service, service)
			}
			return nil, "", &UIError{service, verb, re.Code, re.Message}
		}
		if errors.Is(err, client.ErrClosed) {
			return nil, "", fmt.Errorf("%w (connection closed)", ErrBusDown)
		}
		return nil, "", fmt.Errorf("%s: %w", verb, err)
	}
	return r.Payload, r.From, nil
}

// ---- wire types: the JSON documented in decision 0017 ----

type UIBox struct {
	L float64 `json:"l"`
	T float64 `json:"t"`
	R float64 `json:"r"`
	B float64 `json:"b"`
}

type UIText struct {
	Text  string  `json:"text"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Width float64 `json:"width"`
	Size  float64 `json:"size"`
	Box   UIBox   `json:"box"`
}

type UIState struct {
	Hidden            bool  `json:"hidden"`
	EffectivelyHidden bool  `json:"effectively_hidden"`
	Clipped           bool  `json:"clipped"`
	Navigable         bool  `json:"navigable"`
	Enabled           *bool `json:"enabled"`
	Focused           *bool `json:"focused"`
}

type UINode struct {
	Token          int            `json:"token"`
	Parent         *int           `json:"parent"`
	Name           string         `json:"name"`
	Role           string         `json:"role"`
	RoleConfidence string         `json:"role_confidence"`
	RoleBasis      string         `json:"role_basis"`
	Frame          UIBox          `json:"frame"`
	Abs            UIBox          `json:"abs"`
	Visible        *UIBox         `json:"visible"`
	State          UIState        `json:"state"`
	Drew           map[string]int `json:"drew"`
	Text           []UIText       `json:"text"`
	Children       []UINode       `json:"children"`
	ChildTokens    []int          `json:"child_tokens"`
}

type UIWindow struct {
	Index   int                `json:"index"`
	Title   string             `json:"title"`
	Kind    string             `json:"kind"`
	Size    struct{ W, H int } `json:"size"`
	Visible bool               `json:"visible"`
	Active  bool               `json:"active"`
	Root    *UINode            `json:"root"`
}

type UIViewer struct {
	Identity string `json:"identity"`
	Kind     string `json:"kind"`
	Rule     string `json:"rule"`
}

type UITree struct {
	Schema  string     `json:"schema"`
	Viewer  UIViewer   `json:"viewer"`
	Service string     `json:"service"`
	Owner   string     `json:"owner"`
	Units   string     `json:"units"`
	Scale   float64    `json:"scale"`
	Windows []UIWindow `json:"windows"`
}

const uiCaveats = `  Only what the application's app_server was sent: what a view drew, where, and its name and flags. Roles are inferred, and "unknown" is an answer.
  A list shows the rows it drew, not the rows it holds; a hidden or never-shown page has no text; whether a control is enabled or focused is not known (null).`

func uiService(app string) (string, bool) {
	if !strings.HasPrefix(app, "ui.") {
		app = "ui." + app
	}
	return app, uiServiceRE.MatchString(app)
}

func fmtBox(b UIBox) string { return fmt.Sprintf("%g,%g,%g,%g", b.L, b.T, b.R, b.B) }

func printUINode(w io.Writer, n UINode, depth int) {
	name := short(n.Name)
	if name == "" {
		name = "(unnamed)"
	}
	flags := ""
	if n.State.EffectivelyHidden {
		flags += " hidden"
	}
	if n.State.Clipped {
		flags += " clipped"
	}
	if n.State.Navigable {
		flags += " focusable"
	}
	fmt.Fprintf(w, "%s%s  [%s/%s]  %s%s\n", strings.Repeat("  ", depth), name, short(n.Role), short(n.RoleConfidence), fmtBox(n.Abs), flags)
	for _, t := range n.Text {
		fmt.Fprintf(w, "%s  \"%s\"\n", strings.Repeat("  ", depth), short(t.Text))
	}
	for _, c := range n.Children {
		printUINode(w, c, depth+1)
	}
}

func cmdUI(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "ui", "ui tree APP [--window N] [--root TOKEN] [--depth N] [--no-text] [--json] | ui node APP WINDOW TOKEN [--json] | ui text APP WINDOW TOKEN [--deep] [--json]", &c)
	window := fs.Int("window", -1, "tree: only this window (its creation index)")
	root := fs.Int("root", -1, "tree: only the subtree under this view token (needs --window)")
	depth := fs.Int("depth", -1, "tree: levels below the start (-1: all, 0: just the start)")
	noText := fs.Bool("no-text", false, "tree: leave out the text each view drew")
	deep := fs.Bool("deep", false, "text: include every descendant, in reading order")
	asJSON := fs.Bool("json", false, "print the service's reply as it is, one JSON object")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	if len(pos) < 2 {
		fmt.Fprintln(env.Err, "aurora ui: want  tree APP | node APP WINDOW TOKEN | text APP WINDOW TOKEN")
		return 2
	}
	sub := pos[0]
	service, ok := uiService(pos[1])
	if !ok {
		fmt.Fprintf(env.Err, "aurora ui: %q is not an application service name\n", clean(pos[1]))
		return 2
	}
	var verb string
	var req map[string]any
	switch sub {
	case "tree":
		if len(pos) != 2 {
			fmt.Fprintln(env.Err, "aurora ui tree: want APP only; use --window and --root to narrow")
			return 2
		}
		verb, req = "tree@v1", map[string]any{"depth": *depth, "text": !*noText}
		if *window >= 0 {
			req["window"] = *window
		}
		if *root >= 0 {
			req["root"] = *root
		}
	case "node", "text":
		if len(pos) != 4 {
			fmt.Fprintf(env.Err, "aurora ui %s: want APP WINDOW TOKEN\n", sub)
			return 2
		}
		var w, t int
		if _, e1 := fmt.Sscanf(pos[2], "%d", &w); e1 != nil {
			fmt.Fprintf(env.Err, "aurora ui %s: WINDOW %q is not a number\n", sub, clean(pos[2]))
			return 2
		}
		if _, e2 := fmt.Sscanf(pos[3], "%d", &t); e2 != nil {
			fmt.Fprintf(env.Err, "aurora ui %s: TOKEN %q is not a number\n", sub, clean(pos[3]))
			return 2
		}
		verb, req = sub+"@v1", map[string]any{"window": w, "token": t}
		if sub == "text" {
			req["deep"] = *deep
		}
	default:
		fmt.Fprintf(env.Err, "aurora ui: unknown subcommand %q (tree, node, text)\n", clean(sub))
		return 2
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	ub, ok := be.(UIBackend)
	if !ok {
		fmt.Fprintln(env.Err, "aurora ui: this backend cannot ask an application for its tree")
		return 1
	}
	raw, from, err := ub.UICall(context.Background(), service, verb, req)
	if err != nil {
		var ue *UIError
		if errors.As(err, &ue) {
			fmt.Fprintf(env.Err, "aurora ui: REFUSED by %s: %s: %s\n", short(service), short(ue.Code), short(ue.Message))
			return 1
		}
		return fail(env, err)
	}
	if *asJSON {
		fmt.Fprintln(env.Out, strings.TrimSpace(string(raw)))
		return 0
	}
	switch sub {
	case "text":
		var t struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return fail(env, fmt.Errorf("malformed reply: %w", err))
		}
		fmt.Fprintln(env.Out, short(strings.ReplaceAll(t.Text, "\n", "⏎")))
		return 0
	case "node":
		var t struct {
			Window int    `json:"window"`
			Node   UINode `json:"node"`
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return fail(env, fmt.Errorf("malformed reply: %w", err))
		}
		printUINode(env.Out, t.Node, 0)
		return 0
	}
	var t UITree
	if err := json.Unmarshal(raw, &t); err != nil {
		return fail(env, fmt.Errorf("malformed reply: %w", err))
	}
	fmt.Fprintf(env.Out, "%s  (served by %s, asked from %s)\n", short(t.Service), short(from), short(t.Viewer.Identity))
	fmt.Fprintf(env.Out, "  VIEW: the service says you are a %s: %s\n", short(t.Viewer.Kind), short(t.Viewer.Rule))
	for _, w := range t.Windows {
		fmt.Fprintf(env.Out, "window %d  %q  %s  %dx%d%s\n", w.Index, short(w.Title), short(w.Kind), w.Size.W, w.Size.H, map[bool]string{true: "  active", false: ""}[w.Active])
		if w.Root != nil {
			printUINode(env.Out, *w.Root, 1)
		}
	}
	fmt.Fprintln(env.Out, uiCaveats)
	return 0
}
