// Test-only prerequisites use actual pinned SDK code; only its network clients record.
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {pathToFileURL} from 'node:url';
import {createRequire} from 'node:module';
import {createHash} from 'node:crypto';
import {Readable} from 'node:stream';
import path from 'node:path';
import net from 'node:net';

export const LOCK_SHA = 'fcb99bf7fb824e8ddb93269eca3f3537427fdbd13c2e556383d3bda2e39f422c';
export const BLOB_SHA = '08cfc257f4a0b488bb0bfcf046c10902119e79fa0fcbfe1df95ea7b690333366';
export const digest = b => createHash('sha256').update(b).digest('hex');
export const token = `${Buffer.from('{"alg":"none"}').toString('base64url')}.${Buffer.from(JSON.stringify({scp:'Actions.Results:11111111-1111-1111-1111-111111111111:22222222-2222-2222-2222-222222222222'})).toString('base64url')}.synthetic`;
export const CANARY = 'synthetic-np-upload-apricot';
export function assertArtifactRequests(records, bytes) {
  const requests=records.filter(r=>r.kind==='request');
  assert.deepEqual(requests.map(r=>r.method),['CreateArtifact','FinalizeArtifact']);
  for(const {body} of requests) {
    assert.equal(body.name,'newman-publication-17-2-0');
    assert.equal(body.workflow_run_backend_id,'11111111-1111-1111-1111-111111111111');
    assert.equal(body.workflow_job_run_backend_id,'22222222-2222-2222-2222-222222222222');
  }
  assert.equal(requests[1].body.size,String(bytes.length));
  assert.equal(requests[1].body.hash,'sha256:'+digest(bytes));
}
export async function sdkImports(home) {
  const pkg = path.join(home, 'node_modules/@actions/artifact');
  assert.equal(JSON.parse(readFileSync(path.join(pkg, 'package.json'))).version, '6.2.1');
  assert.equal(digest(readFileSync(path.join(home, 'package-lock.json'))), LOCK_SHA);
  assert.equal(digest(readFileSync(path.join(pkg, 'lib/internal/upload/blob-upload.js'))), BLOB_SHA);
  const req = createRequire(path.join(home, 'package.json'));
  const at = f => import(pathToFileURL(path.join(pkg, f)).href);
  return {
    ...await at('lib/internal/upload/blob-upload.js'),
    ...await at('lib/internal/upload/stream.js'),
    ...await at('lib/internal/shared/artifact-twirp-client.js'),
    ...await at('lib/internal/shared/util.js'),
    ...await at('lib/generated/index.js'),
    BlobClient: (await import(pathToFileURL(path.join(home, 'node_modules/@azure/storage-blob/dist/esm/index.js')).href)).BlobClient,
    HttpClient: (await import(pathToFileURL(path.join(home, 'node_modules/@actions/http-client/lib/index.js')).href)).HttpClient,
  };
}
export async function installRecordingNetwork(home, record, mode = 'lawful') {
  const sdk = await sdkImports(home);
  const oldBlob = sdk.BlobClient.prototype.getBlockBlobClient;
  const oldPost = sdk.HttpClient.prototype.post;
  sdk.HttpClient.prototype.post = async function (url, data) {
    const method = new URL(url).pathname.split('/').at(-1);
    assert.ok(['CreateArtifact', 'FinalizeArtifact'].includes(method));
    const body = JSON.parse(data);
    record({kind:'request', method, body});
    // A public SDK diagnostic is deliberately observable before entrypoint suppression.
    process.stdout.write(CANARY + '-sdk-stdout\n');
    process.stderr.write(CANARY + '-sdk-stderr\n');
    const answer = method === 'CreateArtifact'
      ? {ok:mode !== 'create-not-ok', signedUploadUrl:'https://example.invalid/container/blob?sig='+CANARY}
      : {ok:mode !== 'finalize-not-ok', artifactId:mode === 'bad-artifact-id' ? '0' : '731'};
    return {message:{statusCode:200, headers:{}}, readBody:async()=>JSON.stringify(answer)};
  };
  sdk.BlobClient.prototype.getBlockBlobClient = function () {
    return {uploadStream:async (stream, _size, _parallel, options) => {
      if (mode === 'blob-error') throw new Error(CANARY + '-peer-error');
      const chunks=[];
      for await (const chunk of stream) chunks.push(Buffer.from(chunk));
      const bytes=Buffer.concat(chunks);
      record({kind:'blob', bytes, size:bytes.length, sha256:digest(bytes)});
      options.onProgress({loadedBytes:bytes.length});
    }};
  };
  return {sdk, restore:()=>{sdk.BlobClient.prototype.getBlockBlobClient=oldBlob;sdk.HttpClient.prototype.post=oldPost;}};
}
export async function sdkPrerequisite(home, bytes) {
  const records=[];let networkCalls=0;const oldConnect=net.Socket.prototype.connect;
  net.Socket.prototype.connect=function(){networkCalls++;throw new Error('UNEXPECTED_PREREQUISITE_NETWORK');};
  const oldToken=process.env.ACTIONS_RUNTIME_TOKEN;const oldURL=process.env.ACTIONS_RESULTS_URL;
  process.env.ACTIONS_RUNTIME_TOKEN=token;process.env.ACTIONS_RESULTS_URL='https://example.invalid';
  const {sdk,restore}=await installRecordingNetwork(home, r=>records.push(r));
  try {
    const ids=sdk.getBackendIdsFromToken();const client=sdk.internalArtifactTwirpClient();
    const made=await client.CreateArtifact({...ids,name:'newman-publication-17-2-0',version:7,mimeType:sdk.StringValue.create({value:'application/zip'})});
    assert.equal(made.ok,true);
    const snapshot=Buffer.from(bytes);const original=Buffer.from(bytes);original.fill(120);
    const stream=new sdk.WaterMarkedUploadStream(4096);
    const uploaded=sdk.uploadToBlobStorage(made.signedUploadUrl,stream,'application/zip');
    Readable.from([snapshot]).pipe(stream);const result=await uploaded;
    const done=await client.FinalizeArtifact({...ids,name:'newman-publication-17-2-0',size:String(result.uploadSize),hash:sdk.StringValue.create({value:'sha256:'+result.sha256Hash})});
    assert.equal(done.ok,true);assert.equal(String(done.artifactId),'731');
    const blob=records.find(r=>r.kind==='blob');assert.deepEqual(blob.bytes,bytes);
    assert.equal(result.sha256Hash,digest(bytes));assert.equal(result.uploadSize,bytes.length);
    assert.deepEqual(records.map(r=>r.kind==='request'?r.method:r.kind),['CreateArtifact','blob','FinalizeArtifact']);
    assertArtifactRequests(records,bytes);
    return {sdk:'@actions/artifact@6.2.1',node:process.version,bytes:bytes.length,digest:digest(bytes),calls:records.length,networkCalls};
  } finally {restore();net.Socket.prototype.connect=oldConnect;for(const [name,value]of [['ACTIONS_RUNTIME_TOKEN',oldToken],['ACTIONS_RESULTS_URL',oldURL]]){if(value===undefined)delete process.env[name];else process.env[name]=value;}}
}
