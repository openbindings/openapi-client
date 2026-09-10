import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { OpenAPIClient } from "./index.js";
import { parseJSON, stringifyJSON } from "@openbindings/json";

const cases = JSON.parse(readFileSync(new URL("../../conformance/json-response-numbers.json", import.meta.url), "utf8")) as {name:string;body:string;expected:string}[];
for (const edition of ["2.0", "3.0.4", "3.1.2", "3.2.0"]) {
 describe(edition, () => {
  for (const status of [200,400]) for (const fixture of cases) {
   it(`${status}: ${fixture.name} preserves received JSON values`, async () => {
    const response = edition === "2.0" ? {description:"value",schema:{}} : {description:"value",content:{"application/json":{schema:{}}}};
    const document = {
     ...(edition === "2.0" ? {swagger:edition,host:"api.example.test",schemes:["https"],produces:["application/json"]} : {openapi:edition,servers:[{url:"https://api.example.test"}]}),
     info:{title:"Numbers",version:"1"}, paths:{"/value":{get:{operationId:"value",responses:{[status]:response}}}},
    };
    const client = await OpenAPIClient.load(document, {fetch:async()=>new Response(fixture.body,{status,headers:{"content-type":"application/json"}})});
    // Retain the old shared corpus unchanged as history of the permitted lossy
    // choice. This implementation's quality gate preserves its source values.
    const expected = fixture.name === "duplicate names" ? '{"n":2}' : fixture.body;
    const check = (value:unknown) => {
     expect(stringifyJSON(value)).toBe(expected);
     expect(stringifyJSON(parseJSON(stringifyJSON(value)))).toBe(expected);
    };
    const result=await client.call("value");
    expect(result.ok).toBe(status === 200);
    check(result.ok ? result.data : result.error);
    const streamed=await client.stream("value");
    expect(streamed.ok).toBe(status === 200);
    if (!streamed.ok) check(streamed.error);
    else {
     const values:unknown[]=[];
     for await (const event of streamed.events) values.push(event.data);
     expect(values).toHaveLength(1);
     check(values[0]);
    }
   });
  }
 });
}
