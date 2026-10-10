use crate::{Code, Diagnostic};
use std::{
    fmt,
    future::poll_fn,
    sync::{
        Arc, Mutex, Weak,
        atomic::{AtomicBool, Ordering},
    },
    task::{Poll, Waker},
};

#[derive(Default)]
struct State {
    cancelled: AtomicBool,
    waiters: Mutex<Vec<Weak<Waiter>>>,
}
#[derive(Default)]
struct Waiter(Mutex<Option<Waker>>);

/// Cooperative local cancellation, shared by clones and independent of an executor.
/// Cancellation wakes every registered waiter. It never asserts that a remote
/// action was undone. Dropping a waiter unregisters it without cancelling others.
#[derive(Clone, Default)]
pub struct Cancellation(Arc<State>);
impl fmt::Debug for Cancellation {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Cancellation")
            .field("cancelled", &self.is_cancelled())
            .finish()
    }
}
impl Cancellation {
    /// Request cancellation and wake all pending local waits. Repeated calls are harmless.
    pub fn cancel(&self) {
        self.0.cancelled.store(true, Ordering::Release);
        let waiters = std::mem::take(&mut *self.0.waiters.lock().unwrap());
        for waiter in waiters.into_iter().filter_map(|w| w.upgrade()) {
            let waker = waiter.0.lock().unwrap().take();
            // A waker may execute arbitrary code; never call it while holding a lock.
            if let Some(waker) = waker {
                waker.wake();
            }
        }
    }
    /// Whether cancellation has been requested.
    pub fn is_cancelled(&self) -> bool {
        self.0.cancelled.load(Ordering::Acquire)
    }
    /// Checkpoint for supplied host work.
    pub fn check(&self) -> Result<(), Diagnostic> {
        if self.is_cancelled() {
            Err(Diagnostic::new(Code::Cancelled))
        } else {
            Ok(())
        }
    }
    /// Wait until cancellation is requested, including when already cancelled.
    ///
    /// Each simultaneously polled wait registers its own waker. Dropping this
    /// future releases that registration. No runtime, polling interval or timer
    /// is required; adapters can select this future against pending I/O.
    pub async fn cancelled(&self) {
        if self.is_cancelled() {
            return;
        }
        let registration = Registration {
            state: self.0.clone(),
            waiter: Arc::new(Waiter::default()),
        };
        {
            let mut waiters = self.0.waiters.lock().unwrap();
            if self.is_cancelled() {
                return;
            }
            waiters.push(Arc::downgrade(&registration.waiter));
        }
        poll_fn(|cx| {
            // Clone/drop can also invoke a custom waker vtable. Keep both out
            // of the critical section, just like wake in cancel().
            let incoming = cx.waker().clone();
            let previous = {
                let mut stored = registration.waiter.0.lock().unwrap();
                if !stored.as_ref().is_some_and(|w| w.will_wake(cx.waker())) {
                    stored.replace(incoming)
                } else {
                    None
                }
            };
            drop(previous);
            // Register before checking: cancel either sees the waker or this
            // acquire observes the cancellation, so no wake can be lost.
            if self.is_cancelled() {
                Poll::Ready(())
            } else {
                Poll::Pending
            }
        })
        .await;
    }
}
struct Registration {
    state: Arc<State>,
    waiter: Arc<Waiter>,
}
impl Drop for Registration {
    fn drop(&mut self) {
        self.state
            .waiters
            .lock()
            .unwrap()
            .retain(|w| w.as_ptr() != Arc::as_ptr(&self.waiter));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{future::Future, sync::atomic::AtomicUsize, task::Wake};
    struct Count(AtomicUsize);
    impl Wake for Count {
        fn wake(self: Arc<Self>) {
            self.0.fetch_add(1, Ordering::Relaxed);
        }
    }
    #[test]
    fn wakes_every_waiter_and_unregisters_dropped_waits() {
        let token = Cancellation::default();
        let count = Arc::new(Count(AtomicUsize::new(0)));
        let waker = Waker::from(count.clone());
        let mut cx = std::task::Context::from_waker(&waker);
        let mut first = Box::pin(token.cancelled());
        let mut second = Box::pin(token.cancelled());
        let mut dropped = Box::pin(token.cancelled());
        assert!(first.as_mut().poll(&mut cx).is_pending());
        assert!(second.as_mut().poll(&mut cx).is_pending());
        assert!(dropped.as_mut().poll(&mut cx).is_pending());
        drop(dropped);
        assert_eq!(token.0.waiters.lock().unwrap().len(), 2);
        token.clone().cancel();
        assert_eq!(count.0.load(Ordering::Relaxed), 2);
        assert!(first.as_mut().poll(&mut cx).is_ready());
        assert!(second.as_mut().poll(&mut cx).is_ready());
        token.cancel();
        assert_eq!(count.0.load(Ordering::Relaxed), 2);
        assert!(
            Box::pin(token.cancelled())
                .as_mut()
                .poll(&mut cx)
                .is_ready()
        );
        assert!(token.0.waiters.lock().unwrap().is_empty());
    }
}
