use std::{ffi::OsString, sync::Arc};

use smithay::{
    desktop::{PopupManager, Space, Window, WindowSurfaceType},
    input::{Seat, SeatState},
    reexports::{
        calloop::{EventLoop, Interest, LoopHandle, LoopSignal, Mode, PostAction, generic::Generic},
        wayland_server::{
            Display, DisplayHandle,
            backend::{ClientData, ClientId, DisconnectReason},
            protocol::wl_surface::WlSurface,
        },
    },
    utils::{Logical, Point},
    wayland::{
        compositor::{CompositorClientState, CompositorState},
        fractional_scale::FractionalScaleManagerState,
        viewporter::ViewporterState,
        image_capture_source::{ImageCaptureSourceState, OutputCaptureSourceState},
        image_copy_capture::{Frame, ImageCopyCaptureState, Session, SessionRef},
        input_method::InputMethodManagerState,
        output::OutputManagerState,
        text_input::TextInputManagerState,
        xwayland_shell::XWaylandShellState,
        selection::{data_device::DataDeviceState, ext_data_control::DataControlState, primary_selection::PrimarySelectionState},
        shell::xdg::{XdgShellState, decoration::XdgDecorationState},
        shm::ShmState,
        socket::ListeningSocketSource,
    },
};

pub struct Aurora {
    pub start_time: std::time::Instant,
    pub socket_name: OsString,
    pub display_handle: DisplayHandle,

    pub space: Space<Window>,
    pub loop_signal: LoopSignal,
    pub loop_handle: LoopHandle<'static, Aurora>,

    // Smithay State
    pub compositor_state: CompositorState,
    pub xdg_shell_state: XdgShellState,
    /// Kept alive: it owns the `zxdg_decoration_manager_v1` global.
    pub xdg_decoration_state: XdgDecorationState,
    pub shm_state: ShmState,
    pub fractional_scale_state: FractionalScaleManagerState,
    pub viewporter_state: ViewporterState,
    pub output_manager_state: OutputManagerState,
    pub seat_state: SeatState<Aurora>,
    pub data_device_state: DataDeviceState,
    pub primary_selection_state: PrimarySelectionState,
    pub data_control_state: DataControlState,
    /// The bridge to the host's clipboard, when there is one (see `crate::clipboard`).
    pub host_clip: Option<crate::clipboard::HostClipboard>,
    pub popups: PopupManager,
    pub xwayland_shell_state: XWaylandShellState,
    /// Xwayland, once `--xwayland` started it.
    pub x: Option<crate::xwayland::XState>,
    /// Kept alive: they own the `text_input_v3` and `input_method_v2` globals.
    pub text_input_state: TextInputManagerState,
    pub input_method_state: InputMethodManagerState,

    pub image_capture_source_state: ImageCaptureSourceState,
    pub output_capture_source_state: OutputCaptureSourceState,
    pub image_copy_capture_state: ImageCopyCaptureState,
    /// Live capture sessions. Holding them keeps them alive.
    pub capture_sessions: Vec<Session>,
    /// Frames waiting for the next rendered frame.
    pub pending_frames: Vec<(SessionRef, Frame)>,

    pub seat: Seat<Self>,
    pub trusted: crate::trusted::Trusted,
    /// The capability-in-use indicator (decision 0016).
    pub indicator: crate::indicator::Indicator,
    pub fx: crate::fx::Fx,
    pub shell: crate::shell::Shell,
    /// Title-bar input and window placement state (see `windeco`).
    pub deco: crate::windeco::DecoInput,
    /// The input config last applied to the seat, so a theme reload only touches it on a real change.
    pub applied_input: Option<crate::fx::theme::Input>,
    /// Name of the keysym the last key press produced under the live keymap (test evidence).
    pub last_keysym: Option<String>,
    /// Buttons whose press the shell's chrome took; their release is taken too.
    pub chrome_buttons: std::collections::HashSet<u32>,
    /// Keys whose press the shell's chrome took (menu, settings); their release is taken too.
    pub chrome_keys: std::collections::HashSet<u32>,
    /// `--frame-stats PATH`: per-frame timings, when asked for.
    pub stats: Option<crate::stats::Stats>,
    /// Frames rendered so far (the headless timer and the winit redraw both count).
    pub frame: u64,
    /// `--test-input frame N` callers: their reply is held until frame `.1` has been rendered.
    pub frame_waiters: Vec<(std::os::unix::net::UnixStream, u64)>,
    /// `--state-file PATH`: window placement memory (see `persist`).
    pub persist: Option<crate::persist::Persist<smithay::reexports::wayland_server::backend::ObjectId>>,
}

impl Aurora {
    pub fn new(event_loop: &mut EventLoop<'static, Self>, display: Display<Self>) -> Self {
        let start_time = std::time::Instant::now();

        let dh = display.handle();

        // Here we initialize implementations of some wayland protocols
        // Some of them require us to implement traits on the Aurora state,
        // you can find those implementations in the `crate::handlers` module

        // Initialize protocols needed for displaying windows
        let compositor_state = CompositorState::new::<Self>(&dh);
        let xdg_shell_state = XdgShellState::new::<Self>(&dh);
        let xdg_decoration_state = XdgDecorationState::new::<Self>(&dh);
        let shm_state = ShmState::new::<Self>(&dh, vec![]);
        let fractional_scale_state = FractionalScaleManagerState::new::<Self>(&dh);
        let viewporter_state = ViewporterState::new::<Self>(&dh);
        let popups = PopupManager::default();
        let xwayland_shell_state = XWaylandShellState::new::<Self>(&dh);
        let text_input_state = TextInputManagerState::new::<Self>(&dh);
        // Any client of this user may be the input method (it sees every key while it holds the grab,
        // as with virtual keyboards); there is no finer identity to filter on here.
        let input_method_state = InputMethodManagerState::new::<Self, _>(&dh, |_client| true);

        let output_manager_state = OutputManagerState::new_with_xdg_output::<Self>(&dh);

        let image_capture_source_state = ImageCaptureSourceState::new();
        let output_capture_source_state = OutputCaptureSourceState::new::<Self>(&dh);
        let image_copy_capture_state = ImageCopyCaptureState::new::<Self>(&dh);

        // Data device is responsible for clipboard and drag-and-drop
        let data_device_state = DataDeviceState::new::<Self>(&dh);
        // Middle-click paste, and the focus-less interface clipboard managers (and the host
        // bridge's tests) use. Every client of this user may use data control, as with the
        // other privileged globals here.
        let primary_selection_state = PrimarySelectionState::new::<Self>(&dh);
        // Not `|_| true`: ext_data_control reads the clipboard with no focus, so a
        // client inside a domain must not get it (crate::clipboard docs). The
        // verdict was reached at connect; the filter only reads it, because
        // asking for credentials in here deadlocks.
        let data_control_state = DataControlState::new::<Self, _>(&dh, Some(&primary_selection_state), |c| {
            c.get_data::<ClientState>().is_some_and(|d| d.may_read_clipboard)
        });

        // A seat is a group of keyboards, pointer and touch devices.
        // A seat typically has a pointer and maintains a keyboard focus and a pointer focus.
        let mut seat_state = SeatState::new();
        let mut seat: Seat<Self> = seat_state.new_wl_seat(&dh, "winit");

        // Notify clients that we have a keyboard, for the sake of the example we assume that keyboard is always present.
        // You may want to track keyboard hot-plug in real compositor.
        seat.add_keyboard(Default::default(), 200, 25).unwrap();

        // Notify clients that we have a pointer (mouse)
        // Here we assume that there is always pointer plugged in
        seat.add_pointer();

        // A space represents a two-dimensional plane. Windows and Outputs can be mapped onto it.
        //
        // Windows get a position and stacking order through mapping.
        // Outputs become views of a part of the Space and can be rendered via Space::render_output.
        let space = Space::default();

        // Setup a wayland socket that will be used to accept clients
        let socket_name = Self::init_wayland_listener(display, event_loop);

        // Get the loop signal, used to stop the event loop
        let loop_signal = event_loop.get_signal();

        Self {
            start_time,
            display_handle: dh,

            space,
            loop_signal,
            loop_handle: event_loop.handle(),
            socket_name,

            compositor_state,
            xdg_shell_state,
            xdg_decoration_state,
            shm_state,
            fractional_scale_state,
            viewporter_state,
            output_manager_state,
            seat_state,
            data_device_state,
            primary_selection_state,
            data_control_state,
            host_clip: None,
            popups,
            xwayland_shell_state,
            x: None,
            text_input_state,
            input_method_state,
            image_capture_source_state,
            output_capture_source_state,
            image_copy_capture_state,
            capture_sessions: Vec::new(),
            pending_frames: Vec::new(),
            seat,
            trusted: Default::default(),
            indicator: Default::default(),
            fx: crate::fx::Fx::new(None),
            shell: crate::shell::Shell::new(),
            deco: Default::default(),
            applied_input: None,
            last_keysym: None,
            chrome_buttons: Default::default(),
            chrome_keys: Default::default(),
            stats: None,
            frame: 0,
            frame_waiters: vec![],
            persist: None,
        }
    }

    fn init_wayland_listener(display: Display<Aurora>, event_loop: &mut EventLoop<Self>) -> OsString {
        // Creates a new listening socket, automatically choosing the next available `wayland` socket name.
        let listening_socket = ListeningSocketSource::new_auto().unwrap();

        // Get the name of the listening socket.
        // Clients will connect to this socket.
        let socket_name = listening_socket.socket_name().to_os_string();

        let loop_handle = event_loop.handle();

        loop_handle
            .insert_source(listening_socket, move |client_stream, _, state| {
                // Inside the callback, you should insert the client into the display.
                //
                // You may also associate some data with the client when inserting the client.
                // Decide clipboard access HERE, from the socket's own peer
                // credentials, not later from inside the global filter:
                // `Client::get_credentials` there re-enters a lock the backend
                // already holds and deadlocks every connecting client.
                let clip = crate::clipboard::peer_may_read_clipboard(&client_stream);
                state
                    .display_handle
                    .insert_client(client_stream, Arc::new(ClientState::new(clip)))
                    .unwrap();
            })
            .expect("Failed to init the wayland event source.");

        // You also need to add the display itself to the event loop, so that client events will be processed by wayland-server.
        loop_handle
            .insert_source(
                Generic::new(display, Interest::READ, Mode::Level),
                |_, display, state| {
                    // Safety: we don't drop the display
                    unsafe {
                        display.get_mut().dispatch_clients(state).unwrap();
                    }
                    Ok(PostAction::Continue)
                },
            )
            .unwrap();

        socket_name
    }

    pub fn surface_under(&self, pos: Point<f64, Logical>) -> Option<(WlSurface, Point<f64, Logical>)> {
        self.space.element_under(pos).and_then(|(window, location)| {
            window
                .surface_under(pos - location.to_f64(), WindowSurfaceType::ALL)
                .map(|(s, p)| (s, (p + location).to_f64()))
        })
    }
}

/// Data associated with a wayland client that connects to Aurora.
/// One instance of this type per client.
#[derive(Default)]
pub struct ClientState {
    pub compositor_state: CompositorClientState,
    /// May this client bind `ext_data_control_manager_v1` and so read
    /// everything the user copies, with no focus and no gesture? Decided once,
    /// at connect, from the peer credentials of its socket (see
    /// `clipboard::peer_may_read_clipboard`). Defaults to false, so a client
    /// whose data is some other type is refused rather than trusted.
    pub may_read_clipboard: bool,
}

impl ClientState {
    pub fn new(may_read_clipboard: bool) -> Self {
        ClientState { compositor_state: CompositorClientState::default(), may_read_clipboard }
    }
}

impl ClientData for ClientState {
    fn initialized(&self, _client_id: ClientId) {}
    fn disconnected(&self, _client_id: ClientId, _reason: DisconnectReason) {}
}
