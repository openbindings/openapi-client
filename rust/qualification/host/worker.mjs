import module from '../bridge/generated/oac_host_qualification_bg.wasm';
import {initSync,HostDocument,live_owners} from '../bridge/generated/oac_host_qualification.js';
import {runHost} from './probes.mjs';
import fetchModule from '../../target/qualification/fetch-consumer/generated/oac_fetch_consumer_bg.wasm';
import {initSync as initFetch,FetchClient,live_owners as fetchOwners,live_js_values} from '../../target/qualification/fetch-consumer/generated/oac_fetch_consumer.js';
import {runFetch} from './fetch-probes.mjs';
initSync({module});
initFetch({module:fetchModule});
export default {async fetch(request,env){if(new URL(request.url).pathname==='/ready')return Response.json({run_id:env.RUN_ID});try{const core=await runHost({HostDocument,live_owners},env.PROBE_BASE,'workerd',env.RUN_ID);const fetch_transport=await runFetch({FetchClient,live_owners:fetchOwners,live_js_values},env.PROBE_BASE,'workerd',env.RUN_ID);return Response.json({...core,fetch_transport,passed:core.passed&&fetch_transport.passed});}catch(e){return Response.json({error:String(e),stack:e.stack},{status:500});}}};
