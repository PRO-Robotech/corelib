// Test-only OS/network recording seam. No publisher/checker implementation is provided.
import {createRequire,syncBuiltinESMExports} from 'node:module';
import {appendFileSync,writeFileSync} from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {installRecordingNetwork,CANARY} from './sdk.mjs';
const require=createRequire(import.meta.url);
const cp=require('node:child_process');const realSpawn=cp.spawn;
const evidence=process.env.CI_NP_UPLOAD_RECORD;
const mode=process.env.CI_NP_UPLOAD_MODE;
const record=r=>appendFileSync(evidence,JSON.stringify(r)+'\n');
// Block every unrecorded external socket; SDK HTTP/blob clients below are the only twins.
const net=require('node:net');net.Socket.prototype.connect=function(){record({kind:'unexpected-network'});throw new Error('UNEXPECTED_TEST_NETWORK');};
const children=new Set();
cp.spawn=function(command,args,options={}) {
  const argv=args??[];
  if (!argv.includes('ci.newman_publication') || !argv.includes('check')) {
    record({kind:'unexpected-child'});throw new Error('UNEXPECTED_TEST_CHILD');
  }
  const env=options.env??process.env;
  const leaked=Object.values(env).filter(v=>String(v).includes(CANARY)||v===process.env.ACTIONS_RUNTIME_TOKEN).length;
  record({kind:'child',canonical:argv.includes('-m')&&argv.includes('--archive')&&argv[argv.indexOf('--archive')+1]==='-',credentialValues:leaked,hasSignal:options.signal instanceof AbortSignal});
  let child;
  if(mode==='stall-checker'||mode==='malformed-checker') {
    child=realSpawn(command,[path.join(path.dirname(fileURLToPath(import.meta.url)),'child.py'),mode],options);
  } else child=realSpawn(command,args,options);
  children.add(child);record({kind:'child-spawned',pid:child.pid});
  child.once('close',(code,signal)=>{children.delete(child);record({kind:'child-closed',pid:child.pid,code,signal});});
  return child;
};
syncBuiltinESMExports();
// Record before emergency cleanup so cleanup cannot fake producer-owned termination.
process.on('exit',()=>{record({kind:'process-exit',liveChildren:[...children].map(c=>c.pid)});for(const c of children)c.kill('SIGKILL');});
await installRecordingNetwork(process.env.CI_NP_SDK_ROOT,r=>{
  if(mode==='mutate-path-after-check'&&r.kind==='request'&&r.method==='CreateArtifact'){writeFileSync(process.env['INPUT_ARCHIVE-PATH'],CANARY);record({kind:'mutated-input-after-check'});}
  if(r.kind==='blob'){writeFileSync(evidence+'.bytes',r.bytes);const {bytes,...safe}=r;record(safe);}else record(r);
},mode);
