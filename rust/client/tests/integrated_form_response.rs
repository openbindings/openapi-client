//! Cross-branch public consumer: recover a schema, reuse an owned form, then
//! dispatch with the operation's response bounds and inspect host evidence.
use dynamic_openapi_client::*;
use std::{
    cell::Cell,
    future::Future,
    task::{Context, Poll, Waker},
};

fn ready<F: Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(value) => value,
        Poll::Pending => panic!("finite supplied callback unexpectedly suspended"),
    }
}

#[test]
fn recovered_form_and_response_contract_compose_across_editions() {
    const MEDIA: &str = "application/x-www-form-urlencoded";
    const SOURCE: &str = r#"{
        "openapi":"VERSION",
        "servers":[{"url":"https://api.example"}],
        "paths":{"/send":{"post":{"operationId":"send",
            "requestBody":{"content":{"application/x-www-form-urlencoded":{
                "schema":{"type":"object","properties":{
                    "p":{"$ref":"HTTPS://DOCS.EXAMPLE/%66ield"}
                }}
            }}},"responses":{"200":{"description":"ok"}}
        }}}
    }"#;
    const RESPONSE: &[u8] = br#"{"id":9007199254740993,"n":1.2300e+2}"#;

    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let limits = Limits {
            body_bytes: 64,
            headers: 4,
            document_bytes: 2048,
            ..Limits::default()
        };
        let document = Document::parse(
            SOURCE.replace("VERSION", version),
            Some("https://docs.example/api"),
            limits.clone(),
        )
        .unwrap();
        let form = Form::new([FormField::new("p", "a b")], limits).unwrap();
        let operation = document.operation_id("send").unwrap();
        let missing = operation.describe_form_field(MEDIA, "p").unwrap_err();
        assert_eq!(missing.code(), Code::ResourceUnavailable, "{version}");
        let need = missing.resource_need().unwrap();
        let preparation_error = operation
            .request()
            .body(Body::Form(form.clone()))
            .prepare()
            .unwrap_err();
        assert_eq!(preparation_error.diagnostic().resource_need(), Some(need));

        let mut builder = DocumentBuilder::from_document(&document);
        builder
            .supply(
                need,
                ResourceInput::at(
                    "https://docs.example/%66ield",
                    br#"{"type":"string"}"#.as_slice(),
                )
                .unwrap(),
            )
            .unwrap();
        let recovered = builder.build(document.entry_resource().id()).unwrap();
        let current = recovered.operation_id("send").unwrap();
        assert!(current.describe_form_field(MEDIA, "p").is_ok());
        assert_eq!(
            operation
                .describe_form_field(MEDIA, "p")
                .unwrap_err()
                .resource_need(),
            Some(need),
            "the original snapshot remains unchanged"
        );
        let direct = current
            .prepare(&Input {
                body: Body::Form(form.clone()),
                ..Input::default()
            })
            .unwrap();
        let fluent = current.request().body(Body::Form(form)).prepare().unwrap();
        for request in [direct, fluent] {
            assert_eq!(request.body(), Some(b"p=a+b".as_slice()));
            let expected_admission = request.response_admission();
            assert_eq!(expected_admission.body_bytes(), 64);
            assert_eq!(expected_admission.header_entries(), 4);
            assert_eq!(expected_admission.header_bytes(), 2048);
            let calls = Cell::new(0);
            let outcome = ready(request.invoke(
                Cancellation::default(),
                &HostCapabilities::programmable(),
                |dispatch, _| {
                    calls.set(calls.get() + 1);
                    assert_eq!(dispatch.target, "https://api.example/send");
                    assert_eq!(dispatch.body.as_deref(), Some(b"p=a+b".as_slice()));
                    assert_eq!(dispatch.response_admission(), expected_admission);
                    async {
                        let mut response = ResponseInput::new(
                            200,
                            vec![Header::new("content-type", "application/json")],
                            DeliveredBody {
                                bytes: RESPONSE.into(),
                                state: BodyState::Complete,
                                provenance: Provenance::HostDelivered,
                            },
                        );
                        response.header_delivery = HeaderDelivery::Complete;
                        response.header_fidelity = HeaderFidelity::HostProcessed;
                        TransportResult {
                            response: Some(response),
                            upload: UploadState::Complete,
                            error_detail: None,
                        }
                    }
                },
            ));
            assert_eq!(calls.get(), 1);
            assert_eq!(
                outcome.replay_assurance(),
                Some(ReplayAssurance::NoReplayDeclared)
            );
            let accepted = policies::complete_2xx_json(outcome).unwrap();
            assert_eq!(
                accepted.json.root().pointer("/id").unwrap().raw(),
                "9007199254740993"
            );
            assert_eq!(
                accepted.json.root().pointer("/n").unwrap().raw(),
                "1.2300e+2"
            );
        }
    }
}
