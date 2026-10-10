//! Additive root-authored R15–R17 fixture checks; the original contract is unchanged.
use super::*;
use crate::schema_facts::{Fact, FactQuery, FactValue, FactWork, SchemaSelection, query};
use serde_json::{Value as Json, json};
use std::{collections::HashSet, sync::Arc};

fn fixtures() -> Json {
    serde_json::from_str(include_str!("../tests/fixtures/schema-fact-repair.json")).unwrap()
}
fn document(value: &Json) -> Document {
    Document::parse(serde_json::to_vec(value).unwrap(), None, Limits::default()).unwrap()
}
fn selection(doc: &Document, names: &[String]) -> SchemaSelection {
    SchemaSelection(names.iter().map(|name| doc.entry_resource().schema_at(&format!("/components/schemas/{name}")).unwrap().key).collect())
}
fn answer(fact: &Fact<FactValue>) -> Json {
    match fact {
        Fact::Known { value: FactValue::Types(types), .. } => json!({"Known":types.iter().map(|t| format!("{t:?}").to_lowercase()).collect::<Vec<_>>()}),
        Fact::Indeterminate { reason, .. } => json!({"Indeterminate":format!("{reason:?}")}),
        Fact::Invalid(error) => json!({"Invalid":format!("{:?}",error.code())}),
        _ => json!({"Unexpected":true}),
    }
}
fn unique_provenance(fact: &Fact<FactValue>) -> bool {
    match fact {
        Fact::Known { provenance, .. } | Fact::Absent { provenance } => provenance.iter().copied().collect::<HashSet<_>>().len()==provenance.len(),
        _ => false,
    }
}
#[test]
fn frozen_shared_dag_controls() {
    let fixtures=fixtures();
    let mut failures=Vec::new();
    for n in [4,8,16,20,32,64] {
        let doc=document(&fixtures[format!("diamond-{n}")]);
        let selected=selection(&doc,&["S0".into()]);
        let token=Cancellation::default();
        let owners=Arc::strong_count(&doc.0);
        let mut work=FactWork::new(&doc.0.limits,&token);
        let observed=query(&doc.0.graph,&selected,FactQuery::Types,&mut work);
        let result=match &observed {Ok(f)=>answer(f),Err(e)=>json!({"Error":format!("{:?}",e.reason())})};
        let unique=observed.as_ref().is_ok_and(unique_provenance);
        println!("FACT_REPAIR_OBSERVATION {}",json!({"control":"R15","n":n,"work":work.used(),"result":result,"unique_provenance":unique}));
        if result!=json!({"Known":["string"]}) || !unique { failures.push(n); }
        if let Ok(Fact::Known{provenance,..})=&observed {
            let mut repeat_work=FactWork::new(&doc.0.limits,&token);
            if let Fact::Known{provenance:repeat,..}=query(&doc.0.graph,&selected,FactQuery::Types,&mut repeat_work).unwrap() {
                assert_eq!(&repeat,provenance);
            } else { panic!("repeat changed outcome"); }
        }
        drop(observed);
        assert_eq!(Arc::strong_count(&doc.0),owners);
        let weak=Arc::downgrade(&doc.0);
        drop(doc);
        assert!(weak.upgrade().is_none());
    }
    assert!(failures.is_empty(),"DAG controls failed: {failures:?}");
}
#[test]
fn frozen_numeric_semantic_controls() {
    let fixtures=fixtures();
    let expected=[json!({"Known":["integer"]}),json!({"Known":["integer"]}),json!({"Known":["integer"]}),json!({"Known":["integer","string"]}),json!({"Known":["number"]}),json!({"Known":["integer"]}),json!({"Indeterminate":"ConflictingFacts"}),json!({"Indeterminate":"ConflictingFacts"}),json!({"Known":["integer"]})];
    let mut failures=Vec::new();
    for version in ["3.1.2","3.2.1"] {
        for (i,expected) in expected.iter().enumerate() {
            let mut fixture=fixtures[format!("numeric-{}",i+1)].clone();
            fixture["openapi"]=json!(version);
            let doc=document(&fixture);
            let token=Cancellation::default();
            let mut work=FactWork::new(&doc.0.limits,&token);
            let observed=answer(&query(&doc.0.graph,&selection(&doc,&["Selected".into()]),FactQuery::Types,&mut work).unwrap());
            println!("FACT_REPAIR_OBSERVATION {}",json!({"control":"R17","case":i+1,"edition":version,"result":observed}));
            if &observed!=expected { failures.push(format!("{version}-{}",i+1)); }
            assert_eq!(doc.entry_resource().root().raw(),serde_json::to_string(&fixture).unwrap());
        }
    }
    let doc=document(&fixtures["legacy-siblings"]);
    let token=Cancellation::default();
    let mut work=FactWork::new(&doc.0.limits,&token);
    assert_eq!(answer(&query(&doc.0.graph,&selection(&doc,&["Selected".into()]),FactQuery::Types,&mut work).unwrap()),json!({"Known":["number"]}));
    assert!(failures.is_empty(),"numeric controls failed: {failures:?}");
}
#[test]
fn frozen_longer_memo_path_and_active_cycle_controls() {
    let fixtures=fixtures();
    let doc=document(&fixtures["memo-longer-path"]);
    let selected=selection(&doc,&["T0".into(),"L0".into()]);
    let token=Cancellation::default();
    for (refs,depth,kind) in [(4,128,Some(LimitKind::ReferenceDepth)),(128,4,Some(LimitKind::SchemaFactDepth)),(5,5,None)] {
        let limits=Limits{reference_steps:refs,schema_fact_depth:depth,..Limits::default()};
        let mut work=FactWork::new(&limits,&token);
        let result=query(&doc.0.graph,&selected,FactQuery::Types,&mut work);
        if let Some(kind)=kind {
            let e=result.err().expect("longer selected path must refuse");
            assert_eq!(e.reason(),Some(&DiagnosticReason::Limit{kind,maximum:4,actual:5}));
            assert!(e.location().is_some_and(|l|l.pointer.starts_with("/components/schemas/")));
            assert!(e.source_context().is_some());
        } else { assert_eq!(answer(&result.unwrap()),json!({"Known":["string"]})); }
    }
    let doc=document(&fixtures["memo-cycle"]);
    let mut work=FactWork::new(&doc.0.limits,&token);
    assert_eq!(answer(&query(&doc.0.graph,&selection(&doc,&["S0".into()]),FactQuery::Types,&mut work).unwrap()),json!({"Indeterminate":"RecursiveFact"}));
}
#[test]
fn frozen_flat_shape_controls() {
    let fixtures=fixtures();
    for n in [32,64,128,256] {
        let doc=document(&fixtures[format!("flat-shape-{n}")]);
        let selected=selection(&doc,&(0..n).map(|i|format!("S{i}")).collect::<Vec<_>>());
        let token=Cancellation::default();
        let mut work=FactWork::new(&doc.0.limits,&token);
        let result=query(&doc.0.graph,&selected,FactQuery::ObjectShape,&mut work).unwrap();
        assert!(unique_provenance(&result));
        let Fact::Known{value:FactValue::Shape(shape),provenance} = result else {panic!("expected Shape")};
        let expected=(0..n).map(|i|format!("common-prefix-{i:05}")).collect::<Vec<_>>();
        assert_eq!(shape.names,expected);
        assert!(shape.open);
        assert!(!shape.incomplete);
        let used=work.used();
        println!("FACT_REPAIR_OBSERVATION {}",json!({"control":"R16","n":n,"work":used,"names":shape.names.len(),"provenance":provenance.len()}));
        let mut twice=selected.0.clone();
        twice.extend_from_slice(&selected.0);
        let mut work=FactWork::new(&doc.0.limits,&token);
        let result=query(&doc.0.graph,&SchemaSelection(twice),FactQuery::ObjectShape,&mut work).unwrap();
        assert!(work.used()>used,"repeated admission must consume work");
        assert!(unique_provenance(&result),"repeated contributions remain idempotent");
        let Fact::Known{value:FactValue::Shape(repeated),..}=result else {panic!("expected repeated Shape")};
        assert_eq!(repeated,shape);
    }
}
#[test]
fn frozen_accumulator_small_shared_budget() {
    let fixtures=fixtures();
    let doc=document(&fixtures["flat-shape-32"]);
    let selected=selection(&doc,&(0..32).map(|i|format!("S{i}")).collect::<Vec<_>>());
    let token=Cancellation::default();
    let limits=Limits{schema_fact_work:256,..Limits::default()};
    let mut work=FactWork::new(&limits,&token);
    let e=query(&doc.0.graph,&selected,FactQuery::ObjectShape,&mut work).err().expect("small shared budget must refuse");
    assert!(matches!(e.reason(),Some(DiagnosticReason::Limit{kind:LimitKind::SchemaFactWork,..})));
    assert!(work.used()>32 && work.used()<=256);
    assert!(e.location().is_some());
}
