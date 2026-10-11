// Direct static-glue author controls; no Browser/Worker or Rust-owner claim.
// Run after copying the packaged host.js to an .mjs module path.
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
const {watch,stop,cleanup,yieldTask}=await import(pathToFileURL(process.argv[2]));
const unhandled=[];
const onUnhandled=error=>unhandled.push(error);
process.on('unhandledRejection',onUnhandled);
const tick=()=>new Promise(resolve=>setImmediate(resolve));
const rows=[];
for (const mode of ['pending','success','reject']) {
  let resolve,reject;
  const original=new Promise((a,b)=>{resolve=a;reject=b;});
  const observed=watch(original);
  let settlements=0;
  observed.promise.then(()=>settlements++);
  stop(observed);stop(observed);
  assert.deepEqual(await observed.promise,[false,undefined]);
  if(mode==='success')resolve({body:'late'});
  if(mode==='reject')reject('secret'.repeat(1024*1024));
  await tick();
  assert.equal(settlements,1);
  rows.push({id:mode==='pending'?'LC-01':mode==='success'?'LC-02':'LC-03',settlements});
}
for (const reject of [false,true]) {
  const original=reject?Promise.reject('private'):Promise.resolve('first');
  const observed=watch(original);
  const result=await observed.promise;
  stop(observed);stop(observed);
  assert.deepEqual(result,reject?[false,undefined]:[true,'first']);
}
rows.push({id:'LC-05',first_resolution_wins:true});
let finishSibling;
const a=watch(new Promise(()=>{}));
const b=watch(new Promise(resolve=>{finishSibling=resolve;}));
stop(a);stop(a);finishSibling('sibling');
assert.deepEqual(await a.promise,[false,undefined]);
assert.deepEqual(await b.promise,[true,'sibling']);
rows.push({id:'LC-06',sibling_isolated:true,scope:'JS observation objects; Rust shared-token control remains separate'});
for (const mode of ['throw','reject']) {
  const calls=[];
  cleanup({abort(){calls.push('abort');throw Error('private');}}, {
    cancel(){calls.push('cancel');if(mode==='throw')throw Error('private');return Promise.reject('private');},
    releaseLock(){calls.push('release');throw Error('private');},
  },true);
  assert.deepEqual(calls,['abort','cancel','release']);
}
rows.push({id:'LC-04',all_attempts:true});
let scheduled=0,cleared=0;
const yielded=yieldTask({global:globalThis,setTimeout(){scheduled++;return 42;},clearTimeout(id){assert.equal(id,42);cleared++;}});
stop(yielded);stop(yielded);
assert.deepEqual(await yielded.promise,[false,undefined]);
assert.equal(scheduled,1);assert.equal(cleared,1);
rows.push({id:'LC-07',timer_cleared:true,scope:'direct JS guard; Rust ready-read loop remains separate'});
const stalled=[];
for(let i=0;i<100;i++){const original=new Promise(()=>{});stalled.push(original);const w=watch(original);stop(w);assert.deepEqual(await w.promise,[false,undefined]);}
assert.equal(stalled.length,100);
rows.push({id:'LC-08',iterations:100,scope:'settlement only; no Rust/JS resource count claim'});
await tick();assert.equal(unhandled.length,0);
process.off('unhandledRejection',onUnhandled);
console.log(JSON.stringify({rows,unhandled_rejections:0,scope:'static JS-only author checks, no timing'}));
