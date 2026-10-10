//! Bounded, test-only HTTP/1.1 recorder. No request headers or bodies enter tracing.
//! All tests use a current-thread runtime: the pool event is emitted while its
//! mutex is held, and the awaiting test resumes after that operation finishes.
use std::{
    collections::BTreeMap,
    fmt,
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt},
    net::TcpListener,
    sync::{mpsc, oneshot, watch},
    task::{JoinHandle, JoinSet},
};
use tracing::{
    Event, Metadata, Subscriber,
    field::{Field, Visit},
    span::{Attributes, Id, Record},
};

pub async fn bounded<T>(future: impl std::future::Future<Output = T>) -> T {
    tokio::time::timeout(Duration::from_secs(10), future)
        .await
        .expect("fixture event did not arrive")
}

#[derive(Clone)]
pub struct PoolEvents {
    messages: Arc<Mutex<Vec<String>>>,
    changes: watch::Sender<usize>,
    ids: Arc<AtomicU64>,
}
impl PoolEvents {
    pub fn new() -> Self {
        let (changes, _) = watch::channel(0);
        Self {
            messages: Arc::default(),
            changes,
            ids: Arc::new(AtomicU64::new(1)),
        }
    }
    pub fn count(&self, text: &str) -> usize {
        self.messages
            .lock()
            .unwrap()
            .iter()
            .filter(|m| m.contains(text))
            .count()
    }
    pub async fn wait(&self, text: &str, count: usize) {
        let mut changes = self.changes.subscribe();
        let result = tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                if self.count(text) >= count {
                    break;
                }
                changes.changed().await.unwrap();
            }
        })
        .await;
        assert!(
            result.is_ok(),
            "missing pool event {text:?} count {count}: {:?}",
            self.messages.lock().unwrap()
        );
    }
}
struct Message(String);
impl Visit for Message {
    fn record_debug(&mut self, field: &Field, value: &dyn fmt::Debug) {
        if field.name() == "message" {
            self.0 = format!("{value:?}");
        }
    }
}
impl Subscriber for PoolEvents {
    fn enabled(&self, metadata: &Metadata<'_>) -> bool {
        metadata.target() == "hyper_util::client::legacy::pool"
    }
    fn new_span(&self, _: &Attributes<'_>) -> Id {
        Id::from_u64(self.ids.fetch_add(1, Ordering::Relaxed))
    }
    fn record(&self, _: &Id, _: &Record<'_>) {}
    fn record_follows_from(&self, _: &Id, _: &Id) {}
    fn event(&self, event: &Event<'_>) {
        let mut message = Message(String::new());
        event.record(&mut message);
        if !["pooling idle connection", "max idle per host"]
            .iter()
            .any(|needle| message.0.contains(needle))
        {
            return;
        }
        let mut messages = self.messages.lock().unwrap();
        assert!(messages.len() < 1024, "bounded pool trace exhausted");
        messages.push(message.0);
        self.changes.send_replace(messages.len());
    }
    fn enter(&self, _: &Id) {}
    fn exit(&self, _: &Id) {}
}

#[derive(Clone, Debug)]
pub struct Request {
    pub connection: usize,
    pub head: String,
    pub body: Vec<u8>,
}
#[derive(Clone, Debug)]
pub enum Observation {
    Accepted(usize),
    TlsSuccess(usize),
    TlsFailure(usize),
    Started(usize),
    Request(Request),
    Prefix(usize, Vec<u8>),
    Written(usize),
    Closed(usize),
}
#[derive(Clone, Copy)]
pub enum ReadMode {
    Full,
    PrefixThenClose(usize),
}
enum Command {
    Reply(Vec<u8>, ReadMode, bool),
    Close,
}
struct State {
    events: Mutex<Vec<Observation>>,
    commands: Mutex<BTreeMap<usize, mpsc::Sender<Command>>>,
    changes: watch::Sender<usize>,
}
impl State {
    fn push(&self, event: Observation) {
        let mut events = self.events.lock().unwrap();
        assert!(events.len() < 1024, "bounded socket recorder exhausted");
        events.push(event);
        self.changes.send_replace(events.len());
    }
}
pub struct Server {
    pub origin: String,
    state: Arc<State>,
    stop: oneshot::Sender<()>,
    task: JoinHandle<()>,
}
trait Socket: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> Socket for T {}
impl Server {
    pub async fn start(tls: bool) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!(
            "{}://{}",
            if tls { "https" } else { "http" },
            listener.local_addr().unwrap()
        );
        let (changes, _) = watch::channel(0);
        let state = Arc::new(State {
            events: Mutex::default(),
            commands: Mutex::default(),
            changes,
        });
        let shared = state.clone();
        let (stop, mut stopped) = oneshot::channel();
        let acceptor = tls.then(tls_acceptor);
        let task = tokio::spawn(async move {
            let mut connections = JoinSet::new();
            let mut id = 0;
            loop {
                tokio::select! {
                    biased;
                    _ = &mut stopped => break,
                    accepted = listener.accept() => {
                        let (stream, _) = accepted.unwrap(); id += 1; assert!(id <= 64, "bounded socket count exhausted");
                        let (commands, receiver) = mpsc::channel(8); shared.commands.lock().unwrap().insert(id, commands); shared.push(Observation::Accepted(id));
                        let state = shared.clone(); let acceptor = acceptor.clone();
                        connections.spawn(async move {
                            let stream: Box<dyn Socket> = if let Some(acceptor) = acceptor {
                                match bounded(acceptor.accept(stream)).await { Ok(stream) => { state.push(Observation::TlsSuccess(id)); Box::new(stream) }, Err(_) => { state.push(Observation::TlsFailure(id)); state.push(Observation::Closed(id)); return; } }
                            } else { Box::new(stream) };
                            serve(id, stream, receiver, state).await;
                        });
                    }
                    result = connections.join_next(), if !connections.is_empty() => { result.unwrap().unwrap(); }
                }
            }
            let commands: Vec<_> = shared.commands.lock().unwrap().values().cloned().collect();
            for sender in commands {
                let _ = sender.send(Command::Close).await;
            }
            while let Some(result) = connections.join_next().await {
                result.unwrap();
            }
        });
        Self {
            origin,
            state,
            stop,
            task,
        }
    }
    pub fn events(&self) -> Vec<Observation> {
        self.state.events.lock().unwrap().clone()
    }
    pub fn requests(&self) -> Vec<Request> {
        self.events()
            .into_iter()
            .filter_map(|e| {
                if let Observation::Request(r) = e {
                    Some(r)
                } else {
                    None
                }
            })
            .collect()
    }
    pub fn accepted(&self) -> Vec<usize> {
        self.events()
            .into_iter()
            .filter_map(|e| {
                if let Observation::Accepted(id) = e {
                    Some(id)
                } else {
                    None
                }
            })
            .collect()
    }
    pub fn closed(&self) -> Vec<usize> {
        self.events()
            .into_iter()
            .filter_map(|e| {
                if let Observation::Closed(id) = e {
                    Some(id)
                } else {
                    None
                }
            })
            .collect()
    }
    pub async fn wait(&self, predicate: impl Fn(&[Observation]) -> bool) {
        let mut changes = self.state.changes.subscribe();
        let result = tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                if predicate(&self.events()) {
                    break;
                }
                changes.changed().await.unwrap();
            }
        })
        .await;
        assert!(
            result.is_ok(),
            "missing socket event: accepted={:?}, requests={}, closed={:?}",
            self.accepted(),
            self.requests().len(),
            self.closed()
        );
    }
    pub async fn request(&self, index: usize) -> Request {
        self.wait(|events| {
            events
                .iter()
                .filter(|e| matches!(e, Observation::Request(_)))
                .count()
                > index
        })
        .await;
        self.requests()[index].clone()
    }
    async fn command(&self, id: usize, command: Command) {
        let sender = self
            .state
            .commands
            .lock()
            .unwrap()
            .get(&id)
            .unwrap()
            .clone();
        sender.send(command).await.unwrap();
    }
    pub async fn reply(&self, id: usize, bytes: Vec<u8>) {
        self.reply_mode(id, bytes, ReadMode::Full, false).await;
    }
    pub async fn reply_mode(&self, id: usize, bytes: Vec<u8>, next: ReadMode, close: bool) {
        self.command(id, Command::Reply(bytes, next, close)).await;
    }
    pub async fn close(&self, id: usize) {
        self.command(id, Command::Close).await;
    }
    pub async fn finish(self) -> Vec<Observation> {
        let _ = self.stop.send(());
        bounded(self.task).await.unwrap();
        self.state.events.lock().unwrap().clone()
    }
}

async fn serve(
    id: usize,
    mut stream: Box<dyn Socket>,
    mut commands: mpsc::Receiver<Command>,
    state: Arc<State>,
) {
    let mut raw = Vec::new();
    let mut header_end = None;
    let mut expected = None;
    let mut awaiting = false;
    let mut mode = ReadMode::Full;
    loop {
        let mut buffer = [0u8; 4096];
        let max = match mode {
            ReadMode::Full => buffer.len(),
            ReadMode::PrefixThenClose(n) => n - raw.len(),
        };
        tokio::select! {
            biased;
            command = commands.recv() => match command {
                Some(Command::Reply(bytes, next, close)) => {
                    assert!(awaiting, "reply before a complete application request");
                    stream.write_all(&bytes).await.unwrap(); stream.flush().await.unwrap(); state.push(Observation::Written(id));
                    raw.clear(); header_end = None; expected = None; awaiting = false; mode = next;
                    if close { break; }
                }
                Some(Command::Close) | None => break,
            },
            read = stream.read(&mut buffer[..max]) => match read {
                Ok(0) | Err(_) => break,
                Ok(n) => {
                    assert!(!awaiting, "unexpected pipelining or repeated request before reply");
                    if raw.is_empty() { state.push(Observation::Started(id)); }
                    raw.extend_from_slice(&buffer[..n]); assert!(raw.len() < 2_000_000);
                    if let ReadMode::PrefixThenClose(limit) = mode { if raw.len() == limit { break; } continue; }
                    if header_end.is_none() {
                        header_end = raw.windows(4).position(|w| w == b"\r\n\r\n").map(|i| i + 4);
                        if let Some(end) = header_end {
                            assert!(end <= 32_768); let head = std::str::from_utf8(&raw[..end]).unwrap();
                            let body: usize = head.lines().find_map(|line| { let (name, value) = line.split_once(':')?; name.eq_ignore_ascii_case("content-length").then(|| value.trim().parse::<usize>().unwrap()) }).unwrap_or(0);
                            assert!(body < 1_500_000); expected = Some(end + body);
                        } else { assert!(raw.len() <= 32_768); }
                    }
                    if let Some(total) = expected { assert!(raw.len() <= total, "unexpected pipelining"); if raw.len() == total {
                        let end = header_end.unwrap(); state.push(Observation::Request(Request { connection: id, head: String::from_utf8(raw[..end].to_vec()).unwrap(), body: raw[end..].to_vec() })); raw.clear(); awaiting = true;
                    } }
                }
            }
        }
    }
    if !raw.is_empty() {
        state.push(Observation::Prefix(id, raw));
    }
    let _ = stream.shutdown().await;
    state.push(Observation::Closed(id));
}
fn tls_acceptor() -> tokio_rustls::TlsAcceptor {
    use tokio_rustls::rustls::{
        ServerConfig,
        pki_types::{CertificateDer, PrivateKeyDer},
    };
    let config = ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(
            vec![CertificateDer::from(
                include_bytes!("../fixtures/server.der").to_vec(),
            )],
            PrivateKeyDer::try_from(include_bytes!("../fixtures/server-key.der").to_vec()).unwrap(),
        )
        .unwrap();
    tokio_rustls::TlsAcceptor::from(Arc::new(config))
}
