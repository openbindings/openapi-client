#[cfg(not(all(target_arch = "wasm32", target_os = "unknown")))]
#[test]
fn native_construction_is_controlled_and_does_not_emulate_fetch() {
    use dynamic_openapi_client::{Code, DiagnosticReason, HostRefusal};
    use dynamic_openapi_client_fetch::{Client, Profile};
    for profile in [Profile::Browser, Profile::Worker] {
        let error = Client::new(profile).unwrap_err();
        assert_eq!(error.code(), Code::HostCapability);
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Host(HostRefusal::Unavailable))
        );
        assert!(error.source_position().is_none());
    }
}
