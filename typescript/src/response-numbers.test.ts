import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { OpenAPIClient } from "./index.js";

const cases = JSON.parse(readFileSync(new URL("../../conformance/json-response-numbers.json", import.meta.url), "utf8")) as {name:string;body:string;expected:string}[];
for (const edition of ["2.0", "3.0.4", "3.1.2", "3.2.0"]) {
 describe(edition, () => {
  for (const status of [200,400]) for (const fixture of cases) {
   it(`${status}: ${fixture.name} stays nearest finite JSON`, async () => {
    const response = edition === "2.0" ? {description:"value",schema:{}} : {description:"value",content:{"application/json":{schema:{}}}};
    const document = {
     ...(edition === "2.0" ? {swagger:edition,host:"api.example.test",schemes:["https"],produces:["application/json"]} : {openapi:edition,servers:[{url:"https://api.example.test"}]}),
     info:{title:"Numbers",version:"1"}, paths:{"/value":{get:{operationId:"value",responses:{[status]:response}}}},
    };
    const client = await OpenAPIClient.load(document, {fetch:async()=>new Response(fixture.body,{status,headers:{"content-type":"application/json"}})});
    const expected: unknown = JSON.parse(fixture.expected);
    const check = (value:unknown) => {
     expect(value).toEqual(expected);
     expect(JSON.parse(JSON.stringify(value))).toEqual(JSON.parse(JSON.stringify(expected)));
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
