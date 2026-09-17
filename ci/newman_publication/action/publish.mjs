// Проверенные байты остаются приватными от чтения до принимающего транспорта.
import {open} from 'node:fs/promises';
import {constants} from 'node:fs';
import {createHash} from 'node:crypto';
import {Readable} from 'node:stream';

const MAX_BYTES = 67108864;
const MAX_TIMEOUT = 120000;
const resultKeys = ['schema_version', 'operation', 'status', 'code', 'files_declared',
  'files_checked', 'fields_checked', 'findings', 'archive_bytes', 'archive_sha256'];
const refusalCodes = new Set(['EMPTY_INPUT', 'MISSING_INPUT', 'UNREADABLE_INPUT',
  'MALFORMED_INPUT', 'UNSUPPORTED_INPUT', 'UNSUPPORTED_ENCODING', 'LIMIT_EXCEEDED',
  'SOURCE_MISMATCH', 'UNSAFE_PATH', 'OUTPUT_EXISTS', 'INTERRUPTED',
  'CHECKER_UNAVAILABLE', 'INTERNAL_ERROR']);
const integer = (n, minimum = 0) => Number.isSafeInteger(n) && n >= minimum;
const shape = (value, keys) => value !== null && typeof value === 'object'
  && !Array.isArray(value) && Object.keys(value).length === keys.length
  && keys.every(k => Object.hasOwn(value, k));
const hash = bytes => createHash('sha256').update(bytes).digest('hex');

function checkerShape(v) {
  if (!shape(v, resultKeys) || v.schema_version !== 1 || v.operation !== 'check') return false;
  if (!['files_declared', 'files_checked', 'fields_checked', 'findings', 'archive_bytes']
    .every(k => integer(v[k]))) return false;
  if (v.archive_sha256 !== null && (typeof v.archive_sha256 !== 'string'
    || !/^[a-f0-9]{64}$/.test(v.archive_sha256))) return false;
  if (v.status === 'CLEAN') return v.code === 'COMPLETE' && v.findings === 0;
  if (v.status === 'FINDING') return v.code === 'SECRET_MATERIAL' && v.findings > 0;
  return v.status === 'NOT_EXECUTED' && refusalCodes.has(v.code);
}

async function snapshot(path, ceiling) {
  let fd;
  try {
    fd = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const stat = await fd.stat();
    if (!stat.isFile()) return {code: 'INPUT_UNAVAILABLE'};
    if (stat.size > ceiling) return {code: 'LIMIT_EXCEEDED'};
    const chunks = [];
    let total = 0;
    for (;;) {
      const chunk = Buffer.alloc(Math.min(65536, ceiling + 1 - total));
      const {bytesRead} = await fd.read(chunk, 0, chunk.length, null);
      if (!bytesRead) break;
      total += bytesRead;
      if (total > ceiling) return {code: 'LIMIT_EXCEEDED'};
      chunks.push(chunk.subarray(0, bytesRead));
    }
    return {bytes: Buffer.concat(chunks, total)};
  } catch {
    return {code: 'INPUT_UNAVAILABLE'};
  } finally {
    if (fd) await fd.close();
  }
}

export async function publishSnapshot(options) {
  let archiveBytes = 0, archiveSha = null;
  const result = (code, status = 'NOT_EXECUTED', artifactId = null) => ({
    schema_version: 1, status, code, artifact_id: artifactId,
    archive_bytes: archiveBytes, archive_sha256: archiveSha,
  });
  try {
    if (!options || typeof options !== 'object' || Array.isArray(options)) return result('INVALID_REQUEST');
    const {archivePath, manifestPath, run, checker, transport, afterCheck,
      maxArchiveBytes = MAX_BYTES, checkerTimeoutMs = MAX_TIMEOUT} = options;
    if (typeof archivePath !== 'string' || !archivePath || archivePath.includes('\0')
      || typeof manifestPath !== 'string' || !manifestPath || manifestPath.includes('\0')
      || !shape(run, ['id', 'attempt', 'shardIndex']) || !integer(run.id, 1)
      || !integer(run.attempt, 1) || !integer(run.shardIndex)
      || !integer(maxArchiveBytes, 1) || maxArchiveBytes > MAX_BYTES
      || !integer(checkerTimeoutMs, 1) || checkerTimeoutMs > MAX_TIMEOUT
      || (afterCheck !== undefined && typeof afterCheck !== 'function')) return result('INVALID_REQUEST');
    const name = `newman-publication-${run.id}-${run.attempt}-${run.shardIndex}`;
    const loaded = await snapshot(archivePath, maxArchiveBytes);
    if (loaded.code) return result(loaded.code);
    const bytes = loaded.bytes;
    archiveBytes = bytes.length;
    archiveSha = hash(bytes);
    if (typeof checker !== 'function') return result('CHECKER_UNAVAILABLE');
    const controller = new AbortController();
    let timer;
    const deadline = new Promise(resolve => {
      timer = setTimeout(() => {
        controller.abort();
        resolve({timeout: true});
      }, checkerTimeoutMs);
    });
    let checked;
    try {
      checked = await Promise.race([
        Promise.resolve().then(() => checker({bytes: Buffer.from(bytes), manifestPath,
          signal: controller.signal})).then(verdict => ({verdict}), () => ({unavailable: true})),
        deadline,
      ]);
    } finally { clearTimeout(timer); }
    if (checked.timeout) return result('CHECKER_TIMEOUT');
    if (checked.unavailable) return result('CHECKER_UNAVAILABLE');
    const v = checked.verdict;
    if (!checkerShape(v)) return result('CHECKER_INVALID');
    if (v.archive_sha256 !== null && (v.archive_sha256 !== archiveSha
      || v.archive_bytes !== archiveBytes)) return result('CHECKER_INVALID');
    if (v.status === 'NOT_EXECUTED') return result('CHECKER_INCOMPLETE');
    if (v.archive_sha256 !== archiveSha || v.archive_bytes !== archiveBytes) return result('CHECKER_INVALID');
    if (v.status === 'FINDING') return result('SECRET_MATERIAL', 'FINDING');
    if (v.files_declared === 0 || v.files_checked !== v.files_declared) return result('CHECKER_INCOMPLETE');
    if (afterCheck) await afterCheck();
    if (typeof transport !== 'function') return result('TRANSPORT_UNAVAILABLE');
    const stream = Readable.from([bytes]);
    let received;
    try {
      received = await transport({name, stream, size: archiveBytes, sha256: archiveSha});
    } catch { return result('TRANSPORT_UNAVAILABLE'); }
    finally { stream.destroy(); }
    if (!shape(received, ['artifactId', 'size', 'sha256']) || !integer(received.artifactId, 1)
      || received.size !== archiveBytes || received.sha256 !== archiveSha) return result('TRANSPORT_MISMATCH');
    return result('COMPLETE', 'PUBLISHED', received.artifactId);
  } catch { return result('INTERNAL_ERROR'); }
}
