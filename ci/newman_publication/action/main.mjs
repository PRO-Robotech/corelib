// Fixed entrypoint: только канонические checker и запиненный SDK.
import {spawn} from 'node:child_process';
import {readFileSync} from 'node:fs';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {createHash} from 'node:crypto';
import {pipeline} from 'node:stream/promises';
import {publishSnapshot} from './publish.mjs';

const directory = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(directory, '../../..');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const lockDigest = 'fcb99bf7fb824e8ddb93269eca3f3537427fdbd13c2e556383d3bda2e39f422c';
const blobDigest = '08cfc257f4a0b488bb0bfcf046c10902119e79fa0fcbfe1df95ea7b690333366';
const fallback = code => ({schema_version: 1, status: 'NOT_EXECUTED', code,
  artifact_id: null, archive_bytes: 0, archive_sha256: null});
const writeResult = process.stdout.write.bind(process.stdout);

// Диагностика зависимостей приватна и ограничена по памяти. Не выводится даже при ошибке.
const privateDiagnostics = [];
let diagnosticBytes = 0;
function capture(chunk, encoding, callback) {
  const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk),
    typeof encoding === 'string' ? encoding : 'utf8');
  const remaining = 65536 - diagnosticBytes;
  if (remaining > 0) {
    const part = Buffer.from(bytes.subarray(0, remaining));
    privateDiagnostics.push(part);
    diagnosticBytes += part.length;
  }
  const done = typeof encoding === 'function' ? encoding : callback;
  if (typeof done === 'function') queueMicrotask(done);
  return true;
}
process.stdout.write = capture;
process.stderr.write = capture;

const children = new Set();
const input = name => process.env['INPUT_' + name.toUpperCase()];
function numberInput(name, defaultValue) {
  const value = input(name) ?? defaultValue;
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)$/.test(value)) throw new Error();
  const number = Number(value);
  if (!Number.isSafeInteger(number)) throw new Error();
  return number;
}

function canonicalChecker({bytes, manifestPath, signal}) {
  // Не наследуем credentials, Git overrides, NODE_OPTIONS или Python import overrides.
  const env = {PATH: process.env.PATH, LANG: 'C.UTF-8', LC_ALL: 'C.UTF-8',
    GOWORK: 'off', PYTHONDONTWRITEBYTECODE: '1', PYTHONNOUSERSITE: '1'};
  if (process.env.TMPDIR) env.TMPDIR = process.env.TMPDIR;
  let resolveClosed;
  const closed = new Promise(resolve => { resolveClosed = resolve; });
  children.add(closed);
  return new Promise((resolve, reject) => {
    let child;
    try {
      child = spawn('python3', ['-m', 'ci.newman_publication', 'check', '--archive', '-',
        '--manifest', manifestPath], {cwd: root, env, signal, killSignal: 'SIGKILL',
        stdio: ['pipe', 'pipe', 'pipe']});
    } catch { children.delete(closed); resolveClosed(); reject(new Error()); return; }
    let stdout = [], size = 0, malformed = false, failure = false;
    child.on('error', () => { failure = true; });
    child.stdin.on('error', () => { failure = true; });
    child.stdout.on('data', chunk => {
      size += chunk.length;
      if (size > 65536) { malformed = true; child.kill('SIGKILL'); }
      else stdout.push(chunk);
    });
    child.stderr.on('data', () => { malformed = true; });
    child.once('close', code => {
      children.delete(closed);
      resolveClosed();
      if (signal.aborted || failure) { reject(new Error()); return; }
      if (malformed || ![0, 1, 3].includes(code)) { resolve({}); return; }
      try {
        const text = new TextDecoder('utf-8', {fatal: true}).decode(Buffer.concat(stdout));
        const verdict = JSON.parse(text);
        if ({CLEAN: 0, FINDING: 1, NOT_EXECUTED: 3}[verdict.status] !== code) resolve({});
        else resolve(verdict);
      } catch { resolve({}); }
    });
    child.stdin.end(bytes);
  });
}

async function fixedTransport() {
  if (!/^v24\./.test(process.version) || !process.env.ACTIONS_RUNTIME_TOKEN
    || !process.env.ACTIONS_RESULTS_URL) throw new Error();
  const endpoint = new URL(process.env.ACTIONS_RESULTS_URL);
  if (endpoint.protocol !== 'https:' || endpoint.username || endpoint.password) throw new Error();
  if (digest(readFileSync(path.join(directory, 'package-lock.json'))) !== lockDigest) throw new Error();
  // ESM public export разрешает import; CommonJS require.resolve этот pin не поддерживает.
  const publicEntry = fileURLToPath(import.meta.resolve('@actions/artifact'));
  const pkg = path.dirname(path.dirname(publicEntry));
  const metadata = JSON.parse(readFileSync(path.join(pkg, 'package.json'), 'utf8'));
  if (metadata.name !== '@actions/artifact' || metadata.version !== '6.2.1') throw new Error();
  if (digest(readFileSync(path.join(pkg, 'lib/internal/upload/blob-upload.js'))) !== blobDigest) throw new Error();
  const at = relative => import(pathToFileURL(path.join(pkg, relative)).href);
  const [{uploadToBlobStorage}, {WaterMarkedUploadStream}, {internalArtifactTwirpClient},
    {getBackendIdsFromToken}, {StringValue}] = await Promise.all([
    at('lib/internal/upload/blob-upload.js'), at('lib/internal/upload/stream.js'),
    at('lib/internal/shared/artifact-twirp-client.js'), at('lib/internal/shared/util.js'),
    at('lib/generated/index.js'),
  ]);
  if (![uploadToBlobStorage, WaterMarkedUploadStream, internalArtifactTwirpClient,
    getBackendIdsFromToken, StringValue?.create].every(f => typeof f === 'function')) throw new Error();
  const ids = getBackendIdsFromToken();
  for (const key of ['workflowRunBackendId', 'workflowJobRunBackendId']) {
    if (typeof ids[key] !== 'string' || !ids[key]) throw new Error();
  }
  const client = internalArtifactTwirpClient();
  return async ({name, stream, size, sha256}) => {
    const created = await client.CreateArtifact({...ids, name, version: 7,
      mimeType: StringValue.create({value: 'application/zip'})});
    if (created.ok !== true || typeof created.signedUploadUrl !== 'string') throw new Error();
    const signed = new URL(created.signedUploadUrl);
    if (signed.protocol !== 'https:' || signed.username || signed.password) throw new Error();
    const uploadStream = new WaterMarkedUploadStream(65536);
    let uploaded;
    try {
      const uploading = uploadToBlobStorage(created.signedUploadUrl, uploadStream, 'application/zip');
      const piping = pipeline(stream, uploadStream);
      [uploaded] = await Promise.all([uploading, piping]);
    } finally { uploadStream.destroy(); }
    // Finalize не должен утверждать иной размер или digest после реальной отправки.
    if (uploaded.uploadSize !== size || uploaded.sha256Hash !== sha256) {
      return {artifactId: 0, size: uploaded.uploadSize, sha256: uploaded.sha256Hash};
    }
    const finalized = await client.FinalizeArtifact({...ids, name, size: String(size),
      hash: StringValue.create({value: 'sha256:' + sha256})});
    if (finalized.ok !== true) throw new Error();
    const id = String(finalized.artifactId);
    const artifactId = /^[1-9][0-9]*$/.test(id) ? Number(id) : 0;
    return {artifactId, size, sha256};
  };
}

async function main() {
  let options;
  try {
    options = {archivePath: input('archive-path'), manifestPath: input('manifest-path'),
      run: {id: numberInput('run-id'), attempt: numberInput('run-attempt'),
        shardIndex: numberInput('shard-index')},
      maxArchiveBytes: numberInput('max-archive-bytes', '67108864'),
      checkerTimeoutMs: numberInput('checker-timeout-ms', '120000')};
  } catch { return fallback('INVALID_REQUEST'); }
  let transport;
  try { transport = await fixedTransport(); }
  catch { return fallback('TRANSPORT_UNAVAILABLE'); }
  try { return await publishSnapshot({...options, checker: canonicalChecker, transport}); }
  finally { await Promise.all([...children]); }
}

let result;
try { result = await main(); }
catch { result = fallback('INTERNAL_ERROR'); }
for (const chunk of privateDiagnostics) chunk.fill(0);
privateDiagnostics.length = 0;
writeResult(JSON.stringify(result) + '\n');
process.exitCode = {PUBLISHED: 0, FINDING: 1, NOT_EXECUTED: 3}[result.status];
