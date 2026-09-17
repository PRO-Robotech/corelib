// Harness constructibility only; no claim about the absent publisher/action.
import assert from 'node:assert/strict';
import {readFileSync,writeFileSync} from 'node:fs';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {sdkPrerequisite} from './sdk.mjs';
assert.match(process.version,/^v24\./,'holder must execute under Node24');
const fixture=JSON.parse(readFileSync(process.argv[2]));
const proof=await sdkPrerequisite(process.env.CI_NP_SDK_ROOT,readFileSync(fixture.fixtures[0].archivePath));
const controller=new AbortController();
const child=spawn('python3',['-c','import sys,signal; print("READY",flush=True); signal.pause()'],{env:{PATH:process.env.PATH},signal:controller.signal});
let aborted=false;child.on('error',e=>{assert.equal(e.name,'AbortError');aborted=true;});
const closed=once(child,'close').catch(e=>{if(e.name!=='AbortError')throw e;return new Promise(resolve=>child.once('close',(...x)=>resolve(x)));});
await once(child.stdout,'data');controller.abort();await closed;
assert.equal(aborted,true);assert.notEqual(child.signalCode,null);assert.throws(()=>process.kill(child.pid,0),{code:'ESRCH'});
const parser=spawn('python3',['-c','import yaml; assert yaml.safe_load("runs: {using: node24}")["runs"]["using"] == "node24"'],{env:{PATH:process.env.PATH}});const [rc]=await once(parser,'close');assert.equal(rc,0);
writeFileSync(process.argv[3],JSON.stringify({...proof,abortObserved:true,realChildReaped:true,yamlParser:true})+'\n');
