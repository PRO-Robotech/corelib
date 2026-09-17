// Run the unchanged candidate action in a private snapshot with genuine pinned dependencies.
import assert from 'node:assert/strict';
import {cpSync,copyFileSync,existsSync,mkdirSync,readFileSync,symlinkSync,writeFileSync} from 'node:fs';
import {spawnSync} from 'node:child_process';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {CANARY,token,digest,LOCK_SHA} from './sdk.mjs';
export const inputs={
  'archive-path':null,'manifest-path':null,'run-id':'17','run-attempt':'2','shard-index':'0',
  'max-archive-bytes':'67108864','checker-timeout-ms':'120000',
};
export function fixedMain({root,base,sdkHome,fixture,mode='lawful',override={},withoutToken=false}) {
  const stage=path.join(base,'stage');mkdirSync(stage,{recursive:true});
  cpSync(path.join(root,'ci'),path.join(stage,'ci'),{recursive:true,filter:p=>!p.includes('/tests/')&&!p.includes('/__pycache__/')&&!p.includes('/node_modules/')});
  const action=path.join(stage,'ci/newman_publication/action');
  assert.equal(digest(readFileSync(path.join(action,'package-lock.json'))),LOCK_SHA,'fixed SDK lock differs from approved subject');
  symlinkSync(path.join(sdkHome,'node_modules'),path.join(action,'node_modules'));
  const main=path.join(action,'main.mjs');const yaml=readFileSync(path.join(action,'action.yml'),'utf8');
  // Parse YAML with the real PyYAML parser; no comment/substring decision.
  const parsed=spawnSync('python3',['-c','import json,sys,yaml;print(json.dumps(yaml.safe_load(sys.stdin.read())))'],{input:yaml,encoding:'utf8',timeout:10000});
  assert.equal(parsed.status,0,'action YAML parser prerequisite');const declaration=JSON.parse(parsed.stdout);
  assert.equal(declaration.runs.using,'node24');assert.equal(path.resolve(action,declaration.runs.main),main);
  assert.deepEqual(Object.keys(declaration.inputs).sort(),Object.keys(inputs).sort());
  const record=path.join(base,'boundary.jsonl');writeFileSync(record,'');
  const inputArchive=path.join(base,'input.zip');copyFileSync(fixture.archivePath,inputArchive);
  const environment={PATH:process.env.PATH,LANG:'C.UTF-8',TMPDIR:process.env.TMPDIR,GOWORK:'off',PYTHONDONTWRITEBYTECODE:'1',
    CI_NP_SDK_ROOT:sdkHome,CI_NP_UPLOAD_RECORD:record,CI_NP_UPLOAD_MODE:mode,
    ACTIONS_RUNTIME_TOKEN:token,ACTIONS_RESULTS_URL:'https://example.invalid/'+CANARY,
    GITHUB_TOKEN:CANARY+'-github',GH_TOKEN:CANARY+'-gh',AWS_SECRET_ACCESS_KEY:CANARY+'-aws',
    NODE_OPTIONS:'--import='+fileURLToPath(new URL('./preload.mjs',import.meta.url))};
  for(const [name,value]of Object.entries({...inputs,'archive-path':inputArchive,'manifest-path':fixture.manifestPath,...override}))environment['INPUT_'+name.toUpperCase()]=value;
  if(withoutToken)delete environment.ACTIONS_RUNTIME_TOKEN;
  const result=spawnSync(process.execPath,[main],{cwd:stage,env:environment,encoding:'utf8',timeout:15000,killSignal:'SIGKILL',maxBuffer:1024*1024});
  const rows=readFileSync(record,'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
  // Parent cleanup is recorded as a harness failure, never counted as child-termination proof.
  const spawned=rows.filter(r=>r.kind==='child-spawned').map(r=>r.pid);
  const closed=new Set(rows.filter(r=>r.kind==='child-closed').map(r=>r.pid));
  for(const pid of spawned)if(!closed.has(pid)){try{process.kill(pid,'SIGKILL');}catch{}}
  writeFileSync(path.join(base,'stdout'),result.stdout??'');writeFileSync(path.join(base,'stderr'),result.stderr??'');
  return {process:result,rows,received:existsSync(record+'.bytes')?readFileSync(record+'.bytes'):null};
}
