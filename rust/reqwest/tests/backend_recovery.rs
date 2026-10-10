//! Deterministic source-layer probe: no network, sleeps, scheduler lottery or benchmark.
//! This uses GET/Empty and a custom outer Never policy, not reqwest's private
//! composition. Pinned source review establishes that wiring; real POST socket
//! tests supplement these deliberately controlled lower-layer interleavings.
//! Dependency upgrades must requalify both this ownership seam and socket tests.
use bytes::Bytes;
use http::{Request, Response, Uri};
use http_body_util::{BodyExt, Empty};
use hyper::{
    body::Incoming,
    rt::{Executor, Read, ReadBufCursor, Write},
};
use hyper_util::client::legacy::{
    Client, Error,
    connect::{Connected, Connection},
};
use std::error::Error as _;
use std::{
    future::{Future, Ready, ready},
    io,
    pin::Pin,
    sync::{Arc, Mutex},
    task::{Context, Poll, Waker},
};
use tower::{
    Service,
    retry::{Policy, Retry},
};

type Task = Pin<Box<dyn Future<Output = ()> + Send>>;
type Req = Request<Empty<Bytes>>;

#[derive(Clone, Default)]
struct ManualExecutor(Arc<Mutex<Vec<Task>>>);
impl<F: Future<Output = ()> + Send + 'static> Executor<F> for ManualExecutor {
    fn execute(&self, future: F) {
        self.0.lock().unwrap().push(Box::pin(future));
    }
}
impl ManualExecutor {
    fn tick(&self) {
        let tasks = std::mem::take(&mut *self.0.lock().unwrap());
        let mut pending = Vec::new();
        let mut cx = Context::from_waker(Waker::noop());
        for mut task in tasks {
            if task.as_mut().poll(&mut cx).is_pending() {
                pending.push(task);
            }
        }
        self.0.lock().unwrap().extend(pending);
    }
    fn tasks(&self) -> usize {
        self.0.lock().unwrap().len()
    }
}
fn drive<F: Future>(future: F, executor: &ManualExecutor) -> F::Output {
    let mut future = Box::pin(future);
    let mut cx = Context::from_waker(Waker::noop());
    for _ in 0..100 {
        if let Poll::Ready(output) = future.as_mut().poll(&mut cx) {
            return output;
        }
        executor.tick();
    }
    panic!("scripted future did not complete after 100 deterministic polls");
}

#[derive(Default)]
struct IoState {
    closed: bool,
    fail_next_write_after: Option<usize>,
    request_buffer: Vec<u8>,
    requests: Vec<Vec<u8>>,
    responses: Vec<u8>,
}
struct ScriptedIo(Arc<Mutex<IoState>>);
impl Connection for ScriptedIo {
    fn connected(&self) -> Connected {
        Connected::new()
    }
}
impl Read for ScriptedIo {
    fn poll_read(
        self: Pin<&mut Self>,
        _: &mut Context<'_>,
        mut buf: ReadBufCursor<'_>,
    ) -> Poll<io::Result<()>> {
        let mut state = self.0.lock().unwrap();
        if state.closed {
            return Poll::Ready(Ok(()));
        }
        if state.responses.is_empty() {
            return Poll::Pending;
        }
        let count = buf.remaining().min(state.responses.len());
        buf.put_slice(&state.responses[..count]);
        state.responses.drain(..count);
        Poll::Ready(Ok(()))
    }
}
impl Write for ScriptedIo {
    fn poll_write(
        self: Pin<&mut Self>,
        _: &mut Context<'_>,
        bytes: &[u8],
    ) -> Poll<io::Result<usize>> {
        let mut state = self.0.lock().unwrap();
        if state.closed {
            return Poll::Ready(Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "scripted stale connection",
            )));
        }
        if let Some(limit) = state.fail_next_write_after.take() {
            let count = bytes.len().min(limit);
            state.request_buffer.extend_from_slice(&bytes[..count]);
            state.closed = true;
            return Poll::Ready(Ok(count));
        }
        state.request_buffer.extend_from_slice(bytes);
        while let Some(end) = state
            .request_buffer
            .windows(4)
            .position(|part| part == b"\r\n\r\n")
        {
            let request: Vec<_> = state.request_buffer.drain(..end + 4).collect();
            state.requests.push(request);
            state
                .responses
                .extend_from_slice(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok");
        }
        Poll::Ready(Ok(bytes.len()))
    }
    fn poll_flush(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
    fn poll_shutdown(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Ok(()))
    }
}
#[derive(Clone, Default)]
struct ScriptedConnector(Arc<Mutex<Vec<Arc<Mutex<IoState>>>>>);
impl Service<Uri> for ScriptedConnector {
    type Response = ScriptedIo;
    type Error = io::Error;
    type Future = Ready<Result<ScriptedIo, io::Error>>;
    fn poll_ready(&mut self, _: &mut Context<'_>) -> Poll<Result<(), io::Error>> {
        Poll::Ready(Ok(()))
    }
    fn call(&mut self, _: Uri) -> Self::Future {
        let state = Arc::new(Mutex::new(IoState::default()));
        self.0.lock().unwrap().push(state.clone());
        ready(Ok(ScriptedIo(state)))
    }
}

#[derive(Clone, Default)]
struct Never(Arc<Mutex<usize>>);
impl Policy<Req, Response<Incoming>, Error> for Never {
    type Future = Ready<()>;
    fn retry(
        &mut self,
        _: &mut Req,
        _: &mut Result<Response<Incoming>, Error>,
    ) -> Option<Self::Future> {
        *self.0.lock().unwrap() += 1;
        None
    }
    fn clone_request(&mut self, request: &Req) -> Option<Req> {
        Some(request.clone())
    }
}
#[derive(Clone)]
struct CountCalls<S> {
    inner: S,
    calls: Arc<Mutex<usize>>,
}
impl<S: Service<Req>> Service<Req> for CountCalls<S> {
    type Response = S::Response;
    type Error = S::Error;
    type Future = S::Future;
    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }
    fn call(&mut self, request: Req) -> Self::Future {
        *self.calls.lock().unwrap() += 1;
        self.inner.call(request)
    }
}
fn request() -> Req {
    Request::builder()
        .uri("http://fixture.invalid/item")
        .body(Empty::new())
        .unwrap()
}

fn scenario(inner_retry: bool, partial_write: bool) -> serde_json::Value {
    let executor = ManualExecutor::default();
    let connector = ScriptedConnector::default();
    let mut builder = Client::builder(executor.clone());
    builder
        .pool_idle_timeout(None)
        .pool_max_idle_per_host(1)
        .retry_canceled_requests(inner_retry);
    let client: Client<_, Empty<Bytes>> = builder.build(connector.clone());
    let calls = Arc::new(Mutex::new(0));
    let never = Never::default();
    let mut service = Retry::new(
        never.clone(),
        CountCalls {
            inner: client,
            calls: calls.clone(),
        },
    );
    let first = drive(service.call(request()), &executor).unwrap();
    assert_eq!(first.status(), 200);
    assert_eq!(
        drive(first.into_body().collect(), &executor)
            .unwrap()
            .to_bytes(),
        b"ok".as_slice()
    );

    // Response future has already scheduled its on_idle task (if needed).
    // Complete that task explicitly; only the persistent connection driver remains.
    for _ in 0..20 {
        executor.tick();
        if executor.tasks() == 1 {
            break;
        }
    }
    assert_eq!(
        executor.tasks(),
        1,
        "only the connection driver should remain"
    );
    assert_eq!(connector.0.lock().unwrap().len(), 1);

    // Checkout and enqueue the second request without polling the connection driver.
    let mut second = Box::pin(service.call(request()));
    let mut cx = Context::from_waker(Waker::noop());
    assert!(second.as_mut().poll(&mut cx).is_pending());
    assert_eq!(
        connector.0.lock().unwrap().len(),
        1,
        "the second request checked out the pooled connection"
    );
    let first_io = connector.0.lock().unwrap()[0].clone();
    assert_eq!(
        first_io.lock().unwrap().requests.len(),
        1,
        "the second request has not written bytes"
    );
    if partial_write {
        first_io.lock().unwrap().fail_next_write_after = Some(7);
    } else {
        first_io.lock().unwrap().closed = true;
    }

    // h1 dispatch polls read before write: stale EOF cancels the queued unsent request.
    let result = drive(second, &executor);
    let outcome = match result {
        Ok(response) => {
            assert!(inner_retry && !partial_write);
            assert_eq!(response.status(), 200);
            assert_eq!(
                drive(response.into_body().collect(), &executor)
                    .unwrap()
                    .to_bytes(),
                b"ok".as_slice()
            );
            "second request succeeded via new connection"
        }
        Err(error) => {
            assert!(!inner_retry || partial_write);
            if partial_write {
                assert!(error.source().is_some());
                "partially written second request failed without retry"
            } else {
                assert!(
                    error
                        .source()
                        .and_then(|source| source.downcast_ref::<hyper::Error>())
                        .is_some_and(|source| source.is_canceled())
                );
                "second request returned cancellation error"
            }
        }
    };
    let connections = connector.0.lock().unwrap();
    let request_counts: Vec<_> = connections
        .iter()
        .map(|io| io.lock().unwrap().requests.len())
        .collect();
    assert_eq!(
        request_counts,
        if inner_retry && !partial_write {
            vec![1, 1]
        } else {
            vec![1]
        }
    );
    let partial_bytes = first_io.lock().unwrap().request_buffer.len();
    assert_eq!(partial_bytes, if partial_write { 7 } else { 0 });
    assert_eq!(
        *calls.lock().unwrap(),
        2,
        "outer service called once per application call"
    );
    serde_json::json!({
        "inner_retry_canceled_requests":inner_retry,
        "outer_policy":"never",
        "fault":if partial_write { "after seven bytes of second request" } else { "after checkout, before second request serialization" },
        "second_request_partial_bytes_on_first_connection":partial_bytes,
        "application_calls":2,
        "outer_service_calls":*calls.lock().unwrap(),
        "outer_policy_classifications":*never.0.lock().unwrap(),
        "connector_calls":connections.len(),
        "requests_written_per_connection":request_counts,
        "outcome":outcome,
        "network_sockets":0,
        "timing_heuristics":false
    })
}
#[test]
fn pinned_h1_recovers_only_never_serialized_requests() {
    let observations = vec![
        scenario(true, false),
        scenario(false, false),
        scenario(true, true),
    ];
    println!("{}", serde_json::to_string_pretty(&observations).unwrap());
}
