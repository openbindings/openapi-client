export async function runHost(api,base,host,runId){
 const cases=[];const record=(name,facts,passed)=>cases.push({id:`${host}-${name}`,group:'P09',executed:true,passed,facts});
 const doc=(path='/echo')=>`{"openapi":"3.1.2","info":{"title":"host fixture","version":"1"},"servers":[{"url":"${base}"}],"paths":{"${path}":{"post":{"operationId":"echo","requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok"}}}}},"x":9007199254740993}`;
 const payload='{"large":9007199254740993,"negative":-0,"fraction":0.12345678901234567890123456789}';
 let calls=0;
 const dispatch=async s=>{calls++;const q=JSON.parse(s);if(new URL(q.target).href!==q.target)throw Error('host target mismatch');const body=q.body_hex===null?undefined:Uint8Array.from(q.body_hex.match(/../g)||[],x=>parseInt(x,16));const r=await fetch(q.target,{method:q.method,headers:q.headers,body,credentials:'omit',redirect:'manual'});const opaque=r.type==='opaqueredirect'||r.status>=300&&r.status<400;const bytes=new Uint8Array(await r.arrayBuffer());if(bytes.length>q.response_limit)throw Error('fixture bound');return JSON.stringify({status:r.status,headers:[...r.headers],body_hex:[...bytes].map(b=>b.toString(16).padStart(2,'0')).join(''),opaque_redirect:opaque,provenance:'decoded',upload:'unknown'});};
 const before=api.live_owners();const d=new api.HostDocument(doc());const exact=d.raw('/x');const inspection=JSON.parse(d.inspect());const op=d.operation('echo');d.free();const p=op.prepare(JSON.stringify({body:{json:payload}}));op.free();
 const req=JSON.parse(p.inspect());record('parse-inspect-prepare',{inspection,target:req.target,method:req.method},inspection.operations.length===1&&req.target===base+'/echo'&&req.method==='POST');
 const out=JSON.parse(await p.invoke(dispatch));record('fixture-dispatch',{out,calls},out.response_status===200&&out.dispatch==='dispatched'&&out.upload==='unknown'&&out.json_raw===payload&&calls===1);
 record('exact-values-bytes',{exact,request_hex:req.body_hex,response_hex:out.raw_hex},exact==='9007199254740993'&&out.json_raw===payload&&req.body_hex===out.raw_hex);p.free();
 for(let i=0;i<100;i++){const d=new api.HostDocument(doc());const o=d.operation('echo');d.free();const r=o.prepare('{}');o.free();if(JSON.parse(r.inspect()).target!==base+'/echo')throw Error('owner lost');r.free();}
 record('retained-owner-dispose',{cycles:100,before,after:api.live_owners()},api.live_owners()===before);
 for(const [name,path,headers] of [['forbidden-header','/echo',[['Host','other.example']]],['encoded-dot-refusal','/%2e/echo',[]]]){const d=new api.HostDocument(doc(path));const o=d.operation('echo');const p=o.prepare(JSON.stringify({headers}));let n=0;const out=JSON.parse(await p.invoke(()=>{n++;throw Error('must not dispatch')}));record(name,{out,callback_count:n},out.error==='HostCapability'&&out.dispatch==='not_dispatched'&&n===0);p.free();o.free();d.free();}
 const rd=new api.HostDocument(doc('/redirect'));const ro=rd.operation('echo');const rp=ro.prepare('{}');const redirected=JSON.parse(await rp.invoke(dispatch));record('manual-redirect-refusal',{out:redirected},redirected.error==='OpaqueRedirect');rp.free();ro.free();rd.free();
 const ready=await(await fetch(base+'/ready')).json();record('fresh-readiness',{ready,expected_run_id:runId},ready.run_id===runId);
 return {cases,passed:cases.every(c=>c.passed),live_after:api.live_owners()};
}
export async function hostBench(api,base){
 const rows=[];
 for(const name of ['normal','wide','stress']){
  const text=await(await fetch(base+`/qualification/fixtures/${name}.json`)).text();const expected={normal:8,wide:1000,stress:5000}[name];
  const actions={core_batch:n=>api.core_batch(text,n),copy_crossings:n=>{let total=0;for(let i=0;i<n;i++)total+=api.copy_len(text);return total;},public_calls:n=>{let total=0;for(let i=0;i<n;i++){if(api.parse_digest(text)!==`${text.length}:${expected}`)throw Error('digest mismatch');total+=expected;}return total;},js_loop:n=>{let total=0;for(let i=0;i<n;i++)total+=text.length;return total;}};
  const series=[];
  for(const [stage,action] of Object.entries(actions)){
   action(2);let n=1;const calibration=[];
   while(true){const t=performance.now();action(n);const ms=performance.now()-t;calibration.push({iterations:n,ms});if(ms>=50)break;if(n>=268435456)throw Error('calibration did not reach timing floor');n*=2;}
   const samples=[];
   for(let sample=0;sample<5;sample++){const t=performance.now();const digest=action(n);const ms=performance.now()-t;const expectedDigest=n*(stage==='core_batch'||stage==='public_calls'?expected:text.length);if(digest!==expectedDigest||ms<=0)throw Error('benchmark work mismatch');samples.push({sample,iterations:n,ms,digest});}
   series.push({stage,calibration,samples});
  }
  rows.push({workload:name,bytes:text.length,operations:expected,series});
 }
 return rows;
}
