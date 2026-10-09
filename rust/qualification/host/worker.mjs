import module from '../bridge/generated/oac_host_qualification_bg.wasm';
import {initSync,HostDocument,live_owners} from '../bridge/generated/oac_host_qualification.js';
import {runHost} from './probes.mjs';
initSync({module});
export default {async fetch(request,env){if(new URL(request.url).pathname==='/ready')return Response.json({run_id:env.RUN_ID});try{return Response.json(await runHost({HostDocument,live_owners},env.PROBE_BASE,'workerd',env.RUN_ID));}catch(e){return Response.json({error:String(e),stack:e.stack},{status:500});}}};
