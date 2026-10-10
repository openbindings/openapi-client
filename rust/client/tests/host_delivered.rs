//! Author replay of the frozen independent prospective controls. This supplied
//! callback layer establishes no actual Fetch call or network-decoding claim.
use dynamic_openapi_client::*;
use serde_json::Value as J;
use std::{
    cell::Cell,
    future::Future,
    task::{Context, Poll, Waker},
};

const SOURCE: &str = r#"{"openapi":"3.2.1","servers":[{"url":"https://example.test"}],"paths":{"/":{"get":{"operationId":"read"}}}}"#;
fn ready<F: Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(value) => value,
        Poll::Pending => panic!("finite supplied callback suspended"),
    }
}
fn unhex(s: &str) -> Vec<u8> {
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
        .collect()
}
fn hex(s: &[u8]) -> String {
    s.iter().map(|b| format!("{b:02x}")).collect()
}
fn text<'a>(j: &'a J, name: &str) -> &'a str {
    j[name].as_str().unwrap()
}
fn provenance(s: &str) -> Provenance {
    match s {
        "HostDelivered" => Provenance::HostDelivered,
        "Decoded" => Provenance::Decoded,
        "Wire" => Provenance::Wire,
        "Unknown" => Provenance::Unknown,
        _ => panic!("unknown fixture provenance"),
    }
}
#[test]
fn frozen_core_controls_preserve_delivered_bytes_and_independent_guards() {
    let controls: J =
        serde_json::from_str(include_str!("fixtures/host_delivered_controls.json")).unwrap();
    let mut count = 0;
    for c in controls["controls"].as_array().unwrap() {
        let id = text(c, "id");
        if id == "BDS-35" {
            continue;
        } // Literal private boundary is in transport.rs.
        let i = &c["input"];
        let e = &c["expected"];
        let mut limits = Limits::default();
        if let Some(l) = i.get("limits") {
            limits.body_bytes = l["body_bytes"].as_u64().unwrap() as usize;
            limits.headers = l["headers"].as_u64().unwrap() as usize;
            limits.document_bytes = l["document_bytes"].as_u64().unwrap() as usize;
            limits.depth = l["depth"].as_u64().unwrap() as usize;
            limits.nodes = l["nodes"].as_u64().unwrap() as usize;
        }
        let request = Document::parse(SOURCE, None, limits)
            .unwrap()
            .operation_id("read")
            .unwrap()
            .request()
            .prepare()
            .unwrap();
        let mut host = HostCapabilities::programmable();
        host.no_retries = i["host_no_retries"].as_bool().unwrap_or(true);
        let replay = if i["host_request_policy"] == "AllowHostManaged" {
            ReplayPolicy::AllowHostManaged
        } else {
            ReplayPolicy::RequireNoReplay
        };
        let calls = Cell::new(0);
        let out = ready(request.invoke_with_replay(
            Cancellation::default(),
            &host,
            replay,
            |_, cancel| {
                calls.set(calls.get() + 1);
                async move {
                    if i["cancelled"].as_bool().unwrap_or(false) {
                        cancel.cancel();
                    }
                    let headers = i["headers"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|h| Header::new(h[0].as_str().unwrap(), h[1].as_str().unwrap()))
                        .collect();
                    let mut response = ResponseInput::new(
                        i["status"].as_u64().unwrap() as u16,
                        headers,
                        DeliveredBody {
                            bytes: unhex(text(i, "body_hex")).into(),
                            state: match text(i, "body_state") {
                                "Complete" => BodyState::Complete,
                                "Truncated" => BodyState::Truncated,
                                "Failed" => BodyState::Failed,
                                _ => unreachable!(),
                            },
                            provenance: provenance(text(i, "provenance")),
                        },
                    );
                    response.header_delivery = match text(i, "header_delivery") {
                        "Complete" => HeaderDelivery::Complete,
                        "Incomplete" => HeaderDelivery::Incomplete,
                        _ => HeaderDelivery::Unspecified,
                    };
                    response.header_fidelity = match text(i, "header_fidelity") {
                        "ExactFields" => HeaderFidelity::ExactFields,
                        "HostProcessed" => HeaderFidelity::HostProcessed,
                        _ => HeaderFidelity::Unknown,
                    };
                    response.opaque_redirect = i["opaque_redirect"].as_bool().unwrap();
                    TransportResult {
                        response: Some(response),
                        upload: if i["upload"] == "Unknown" {
                            UploadState::Unknown
                        } else {
                            UploadState::Complete
                        },
                        error_detail: i["transport_error"].as_str().map(Into::into),
                    }
                }
            },
        ));
        count += 1;
        if id == "BDS-31" {
            assert_eq!(calls.get(), 0, "{id}");
            assert_eq!(out.replay_assurance(), None);
            assert_eq!(
                out.error.unwrap().reason(),
                Some(&DiagnosticReason::Host(HostRefusal::Replay))
            );
            continue;
        }
        assert_eq!(calls.get(), 1, "{id}");
        let response = out.response.as_ref().unwrap();
        assert_eq!(
            response.provenance(),
            provenance(text(i, "provenance")),
            "{id}"
        );
        assert_eq!(
            response.status(),
            i["status"].as_u64().unwrap() as u16,
            "{id}"
        );
        assert_eq!(out.cancelled, i["cancelled"].as_bool().unwrap(), "{id}");
        let expected_raw = e
            .get("retained_hex")
            .unwrap_or(&i["body_hex"])
            .as_str()
            .unwrap();
        assert_eq!(hex(response.raw()), expected_raw, "{id}");
        if let Some(state) = e["body_state"].as_str() {
            assert_eq!(format!("{:?}", response.body_state()), state, "{id}");
        }
        if let Some(omitted) = e["core_omissions"].as_u64() {
            assert_eq!(response.omitted_header_lines(), omitted as usize, "{id}");
        }
        if let Some(value) = e["coding_metadata_retained"].as_str() {
            assert!(
                response
                    .headers()
                    .iter()
                    .any(|h| h.name() == "content-encoding" && h.value() == value.as_bytes()),
                "{id}"
            );
        }
        if let Some(expected) = e.get("outcome_error") {
            assert_eq!(
                out.error.as_ref().map(|x| format!("{:?}", x.code())),
                expected.as_str().map(str::to_owned),
                "{id}"
            );
        }
        if let Some(code) = e["json_code"].as_str().or(e["decode_error"].as_str()) {
            let error = response.json().unwrap_err();
            assert_eq!(format!("{:?}", error.code()), code, "{id}");
            if e["safe_default_formatting"] == true {
                let formatted = format!("{error} {error:?} {error:#?}");
                assert!(!formatted.contains("\"ok\""), "{id}");
            }
        } else {
            let json = response.json().unwrap();
            assert_eq!(
                hex(json.root().raw().as_bytes()),
                text(i, "body_hex"),
                "{id}"
            );
            if id == "BDS-33" {
                let projected: std::collections::BTreeMap<String, u64> =
                    json.deserialize().unwrap();
                assert_eq!(projected["n"], 9_007_199_254_740_993);
                assert!(json.root().raw().contains("9007199254740993"));
            }
        }
        let received = policies::received_2xx_json(out.clone());
        let expected_policy = e["received_policy"]
            .as_str()
            .or(e["policy_primary"].as_str())
            .unwrap_or("accepted"); // BDS-33 only registers exact numeric success.
        match received {
            Ok(accepted) => {
                assert_eq!(expected_policy, "accepted", "{id}");
                assert_eq!(
                    accepted.outcome.response.unwrap().provenance(),
                    provenance(text(i, "provenance")),
                    "{id}"
                );
            }
            Err(refused) => {
                assert_eq!(format!("{:?}", refused.reason), expected_policy, "{id}");
                if let Some(raw) = e["decoded_hex"].as_str() {
                    assert_eq!(
                        hex(refused.decoded.unwrap().root().raw().as_bytes()),
                        raw,
                        "{id}"
                    );
                }
            }
        }
        if let Some(policy) = e["strict_policy"].as_str() {
            let refused = policies::complete_2xx_json(out.clone()).unwrap_err();
            assert_eq!(format!("{:?}", refused.reason), policy, "{id}");
            if let Some(raw) = e["strict_refusal_decoded_hex"].as_str() {
                assert_eq!(
                    hex(refused.decoded.unwrap().root().raw().as_bytes()),
                    raw,
                    "{id}"
                );
            }
        }
        if id == "BDS-32" {
            assert_eq!(out.replay_assurance(), Some(ReplayAssurance::HostManaged));
        }
        println!("{id}: core supplied-evidence assertions passed; host execution not asserted");
    }
    assert_eq!(count, 35);
}
