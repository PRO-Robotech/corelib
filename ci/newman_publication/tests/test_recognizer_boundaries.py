"""Независимый CLI holder finite recognition agreement 5717201675.

verifies: https://github.com/PRO-Robotech/kacho/issues/1810

Новые ожидания берутся из proposal49bee310 + grammara00c2aa9 и root event,
не из private retention или реализации. Product imports отсутствуют. Старые
fixtures/readers проверяются до SUT assertions, все процессы реальны. После
неудачной projection зависимый check/reader помечается неисполненным, не PASS.
"""
import base64
import copy
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

from carrier_corpus import encode, zip_bytes
from recognizer_cases import SECRET, accounting_cases, cases
from support import EVIDENCE, Fixture, digest, fixture_hash_check, invoke
from test_carrier_scanner import result_errors
import zipfile


def nodes(value):
    """Только число узлов уже типизированного JSON, без recognizer/decoder."""
    if isinstance(value, dict):
        return 1 + sum(nodes(v) for v in value.values())
    if isinstance(value, list):
        return 1 + sum(nodes(v) for v in value)
    return 1


def forbidden_bytes(data):
    forms = (SECRET.encode(), base64.b64encode(SECRET.encode()),
             ''.join('%%%02X' % b for b in SECRET.encode()).encode(),
             ''.join('\\u%04x' % ord(c) for c in SECRET).encode())
    return any(form in data for form in forms)


class RecognizerBoundaries(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix='np-recognizer-', dir='/var/tmp')
        cls.addClassCleanup(cls.temp.cleanup)
        cls.base = Path(cls.temp.name)
        cls.inputs = EVIDENCE / 'recognizer-inputs'
        cls.inputs.mkdir()
        cls.rows = []
        cls.known = {}
        cls.prerequisites = []
        try:
            fixture_hash_check()
            for scene, expected in [('green', 0), ('finding', 1), ('precondition', 3), ('script-error', 1)]:
                fixture = Fixture(cls.base / ('baseline-' + scene), scene)
                before = fixture.reader_verdict(suffix='baseline')
                assert before['coverage_rc'] == 0 and before['suite_rc'] == expected
                raw = json.loads(fixture.report.read_bytes())['run']
                assert before['live_counts']['requests'] == raw['stats']['requests']['total']
                assert before['live_counts']['assertions'] == raw['stats']['assertions']['total']
                result = fixture.project()
                errors, verdict = result_errors(result, 'project', 'CLEAN', 'COMPLETE')
                assert not errors, errors
                package = (fixture.output / 'publication.zip').read_bytes()
                check = invoke([sys.executable, '-m', 'ci.newman_publication', 'check', '--archive',
                                fixture.output / 'publication.zip', '--manifest', fixture.manifest], label='recognizer-prerequisite-check')
                errors, _ = result_errors(check, 'check', 'CLEAN', 'COMPLETE', package)
                assert not errors, errors
                with zipfile.ZipFile(io.BytesIO(package)) as bundle:
                    projected = bundle.read('reports/report-000001.json')
                after = fixture.reader_verdict(projected, 'baseline-public')
                assert after == before
                cls.known[scene] = fixture, before, projected
                cls.prerequisites.append(dict(scene=scene, reader_before=before, reader_after=after,
                    project_capture=str(result.capture_dir), check_capture=str(check.capture_dir),
                    raw_sha256=digest(fixture.report.read_bytes()), projected_sha256=digest(projected)))
        except Exception as error:
            (EVIDENCE / 'recognizer-prerequisites.json').write_bytes(encode(dict(
                status='NOT_EXECUTED', completed=len(cls.prerequisites), cause=type(error).__name__,
                rows=cls.prerequisites)))
            raise
        (EVIDENCE / 'recognizer-prerequisites.json').write_bytes(encode(dict(
            status='PASS', completed=len(cls.prerequisites), rows=cls.prerequisites)))

    def fixture(self, name, scene='green', *, compact=False):
        original, expected, projected = self.known[scene]
        f = copy.copy(original)
        f.scene = scene
        f.base = self.base / name
        f.raw = f.base / 'raw'
        f.raw.mkdir(parents=True)
        f.output = f.base / 'published'
        f.report = f.raw / 'fixture.json'
        f.report.write_bytes(projected if compact else original.report.read_bytes())
        f.data = copy.deepcopy(original.data)
        f.data['input_root'] = str(f.raw)
        f.manifest = f.base / 'private-manifest.json'
        f.save_manifest()
        return f, expected

    def observe(self, name, operation, process, status, code, package=None, partial=False):
        errors, verdict = result_errors(process, operation, status, code, package)
        if forbidden_bytes(process.stdout + process.stderr):
            errors.append('private_material_in_diagnostic')
        if partial and (not isinstance(verdict, dict) or verdict.get('findings', 0) < 1):
            errors.append('protected_field_partial_finding_lost')
        row = dict(id=name, operation=operation, expected_status=status, expected_code=code,
                   rc=process.returncode, observed=verdict, errors=errors, capture=str(process.capture_dir))
        self.rows.append(row)
        return row

    def final_projection(self, f, before, row):
        """Читатели и числовые факты проверяют продуктовый ZIP, не свой projector."""
        path = f.output / 'publication.zip'
        dependent = dict(check_attempts=0, reader_after_executed=False, status='BLOCKED_BY_PROJECT')
        row['dependent'] = dependent
        if not path.exists() or not isinstance(row['observed'], dict) or row['observed'].get('status') != 'CLEAN':
            if path.exists():
                row['errors'].append('publication_on_refusal')
            return
        data = path.read_bytes()
        (Path(row['capture']) / 'publication.zip').write_bytes(data)
        if (row['observed']['archive_bytes'], row['observed']['archive_sha256']) != (len(data), digest(data)):
            row['errors'].append('project_archive_binding')
        if json.loads((f.output / 'verdict.json').read_bytes()) != row['observed']:
            row['errors'].append('public_verdict_binding')
        if forbidden_bytes((f.output / 'verdict.json').read_bytes()):
            row['errors'].append('private_material_in_public_verdict')
        for mode in ('file', 'stdin'):
            args = [sys.executable, '-m', 'ci.newman_publication', 'check', '--manifest', f.manifest,
                    '--archive', path if mode == 'file' else '-']
            process = invoke(args, input_bytes=data if mode == 'stdin' else None, label='recognizer-final-' + mode)
            self.observe(row['id'] + '-' + mode, 'check', process, 'CLEAN', 'COMPLETE', data)
            dependent['check_attempts'] += 1
        with zipfile.ZipFile(io.BytesIO(data)) as bundle:
            wanted = {'manifest.json', 'reports/report-000001.json'}
            if f.data['logs']:
                wanted.add('logs/log-000001.json')
            if set(bundle.namelist()) != wanted or len(bundle.namelist()) != len(wanted):
                row['errors'].append('complete_output_member_census')
            if bundle.comment:
                row['errors'].append('archive_comment')
            for info in bundle.infolist():
                content = bundle.read(info)
                if info.extra or info.comment or forbidden_bytes(info.filename.encode() + content):
                    row['errors'].append('opaque_metadata_or_content')
                if str(f.base).encode() in content:
                    row['errors'].append('private_path_in_projection')
            projected = bundle.read('reports/report-000001.json')
            if json.loads(projected) != json.loads(self.known[f.scene][2]):
                row['errors'].append('opaque_input_changed_closed_report')
            if f.data['logs']:
                raw = (f.raw / 'probe.log').read_bytes()
                expected = {'schema_version': 1, 'kind': 'log', 'index': 0,
                            'bytes': len(raw), 'lines': len(raw.splitlines())}
                if json.loads(bundle.read('logs/log-000001.json')) != expected:
                    row['errors'].append('log_byte_line_census')
        original = json.loads(f.report.read_bytes())['run']
        public = json.loads(projected)['run']
        if original['stats'] != public['stats'] or len(original['failures']) != len(public['failures']):
            row['errors'].append('run_facts_changed')
        if len(original['executions']) != len(public['executions']):
            row['errors'].append('execution_census')
        for raw, out in zip(original['executions'], public['executions']):
            for key in ('position', 'iteration', 'length'):
                if raw['cursor'][key] != out['cursor'][key]:
                    row['errors'].append('cursor_fact_changed')
            if (raw.get('response') or {}).get('code') != (out.get('response') or {}).get('code'):
                row['errors'].append('http_fact_changed')
        after = f.reader_verdict(projected, 'public')
        dependent.update(reader_after_executed=True, reader_after=after, status='EXECUTED')
        if after != before:
            row['errors'].append('actual_reader_verdict_changed')

    def test_finite_recognition_and_declared_basic_sources(self):
        declared = cases()
        (EVIDENCE / 'recognizer-declared.json').write_bytes(encode(declared))
        start = len(self.rows)
        for case in declared:
            name = case['id']
            f, expected = self.fixture(name, case['scene'])
            if case['context'] == 'log':
                (f.raw / 'probe.log').write_text(case['payload'])
                f.data['logs'] = [{'index': 0, 'path': 'probe.log'}]
            else:
                doc = json.loads(f.report.read_bytes())
                doc['recognition_note'] = case['payload']
                f.report.write_bytes(encode(doc))
            f.save_manifest()
            before = f.reader_verdict(suffix='raw')
            self.assertEqual(before, expected, name + ': fixture reader prerequisite changed')
            members = [('fixture.json', f.report.read_bytes())]
            if f.data['logs']:
                members.append(('probe.log', (f.raw / 'probe.log').read_bytes()))
            package = zip_bytes(members)
            path = self.inputs / (name + '.zip')
            path.write_bytes(package)
            process = invoke([sys.executable, '-m', 'ci.newman_publication', 'scan', '--archive', path], label='recognizer-scan')
            scanned = self.observe(name, 'scan', process, case['status'], case['code'], package,
                                   partial=case['partial_finding'])
            scanned.update(pair=case['pair'], side=case['side'], input_sha256=digest(package), reader_before=before)
            process = f.project()
            incomplete = case['status'] == 'NOT_EXECUTED'
            projected = self.observe(name, 'project', process, 'NOT_EXECUTED' if incomplete else 'CLEAN',
                                     case['code'] if incomplete else 'COMPLETE')
            projected.update(pair=case['pair'], side=case['side'], raw_report_sha256=digest(f.report.read_bytes()))
            if isinstance(projected['observed'], dict) and projected['observed'].get('status') == 'CLEAN':
                if projected['observed']['files_declared'] != len(members) or projected['observed']['files_checked'] != len(members):
                    projected['errors'].append('raw_file_census')
            self.final_projection(f, before, projected)
        self.finish('recognition', start, len(declared))

    def limit_scan(self, name, value, limits, status, code):
        data = zip_bytes([('a.json', encode(value))])
        path = self.inputs / (name + '.zip'); path.write_bytes(data)
        cfg = self.inputs / (name + '.limits.json'); cfg.write_bytes(encode(limits))
        process = invoke([sys.executable, '-m', 'ci.newman_publication', 'scan', '--archive', path,
                          '--limits', cfg], label='recognizer-budget-scan')
        return self.observe(name, 'scan', process, status, code, data)

    def test_representation_node_and_depth_boundaries(self):
        start = len(self.rows)
        examples = accounting_cases()
        (EVIDENCE / 'recognizer-accounting-declared.json').write_bytes(encode(examples))
        for name, value, total_nodes, depth in examples:
            if depth > 8:
                self.limit_scan(name, value, {}, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')
                f, expected = self.fixture('budget-' + name, compact=True)
                original = json.loads(f.report.read_bytes())
                original['recognition_note'] = value
                f.report.write_bytes(encode(original))
                self.assertEqual(f.reader_verdict(suffix='raw'), expected)
                row = self.observe(name, 'project', f.project(), 'NOT_EXECUTED', 'LIMIT_EXCEEDED')
                if (f.output / 'publication.zip').exists():
                    row['errors'].append('publication_above_default_depth')
                continue
            for side, bound, status, code in [('equal', total_nodes, 'CLEAN', 'COMPLETE'),
                                              ('below', total_nodes - 1, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')]:
                # Минимум конфигурации — 1: ноль проверял бы malformed config,
                # а не обход. Для N=1 нет допустимого меньшего предела.
                if bound >= 1:
                    self.limit_scan(name + '-nodes-' + side, value, {'decoded_nodes': bound}, status, code)
            if depth > 1:
                for side, bound, status, code in [('equal', depth, 'CLEAN', 'COMPLETE'),
                                                  ('below', depth - 1, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')]:
                    self.limit_scan(name + '-depth-' + side, value, {'decode_depth': bound}, status, code)
            # Закрытый отчёт, подтверждённый readers, исключает посторонние
            # opaque carriers. Число JSON nodes вычисляется независимо,
            # без использования SUT fields_checked или результатов бюджетов.
            f, expected = self.fixture('budget-' + name, compact=True)
            original = json.loads(f.report.read_bytes())
            baseline_nodes = nodes(original)
            original['recognition_note'] = value
            f.report.write_bytes(encode(original))
            before = f.reader_verdict(suffix='raw')
            self.assertEqual(before, expected)
            for side, bound, status, code in [('equal', baseline_nodes + total_nodes, 'CLEAN', 'COMPLETE'),
                                              ('below', baseline_nodes + total_nodes - 1, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')]:
                cfg = self.inputs / (name + '-project-nodes-' + side + '.json')
                cfg.write_bytes(encode({'decoded_nodes': bound}))
                f.output = f.base / ('publication-' + side)
                row = self.observe(name + '-project-nodes-' + side, 'project', f.project('--limits', cfg), status, code)
                row['accounting'] = dict(report_nodes=baseline_nodes, payload_nodes=total_nodes, bound=bound)
                if side == 'equal':
                    self.final_projection(f, before, row)
                    package_path = f.output / 'publication.zip'
                    if package_path.exists() and row['observed'].get('status') == 'CLEAN':
                        data = package_path.read_bytes()
                        with zipfile.ZipFile(io.BytesIO(data)) as bundle:
                            closed_nodes = sum(nodes(json.loads(bundle.read(info))) for info in bundle.infolist())
                        for check_side, check_bound, check_status, check_code in [('equal', closed_nodes, 'CLEAN', 'COMPLETE'),
                                                                               ('below', closed_nodes - 1, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')]:
                            check_cfg = self.inputs / (name + '-check-nodes-' + check_side + '.json')
                            check_cfg.write_bytes(encode({'decoded_nodes': check_bound}))
                            result = invoke([sys.executable, '-m', 'ci.newman_publication', 'check', '--manifest', f.manifest,
                                             '--archive', package_path, '--limits', check_cfg], label='recognizer-budget-check')
                            checked = self.observe(name + '-check-nodes-' + check_side, 'check', result, check_status, check_code, data)
                            checked['accounting'] = dict(closed_nodes=closed_nodes, bound=check_bound)
                elif (f.output / 'publication.zip').exists():
                    row['errors'].append('publication_below_node_limit')
            if depth > 1:
                for side, bound, status, code in [('equal', depth, 'CLEAN', 'COMPLETE'),
                                                  ('below', depth - 1, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')]:
                    cfg = self.inputs / (name + '-project-depth-' + side + '.json')
                    cfg.write_bytes(encode({'decode_depth': bound}))
                    f.output = f.base / ('depth-' + side)
                    row = self.observe(name + '-project-depth-' + side, 'project', f.project('--limits', cfg), status, code)
                    if side == 'equal':
                        self.final_projection(f, before, row)
                    elif (f.output / 'publication.zip').exists():
                        row['errors'].append('publication_below_depth_limit')
        # Общий бюджет пересекает members; граница файла его не обнуляет.
        value = {'encoding': 'base64', 'data': base64.b64encode(b'"hello').decode()}
        data = zip_bytes([('a.json', encode(value)), ('b.json', encode(value))])
        path = self.inputs / 'nodes-across-members.zip'; path.write_bytes(data)
        for bound, status, code in [(8, 'CLEAN', 'COMPLETE'), (7, 'NOT_EXECUTED', 'LIMIT_EXCEEDED')]:
            cfg = self.inputs / ('nodes-across-members-' + str(bound) + '.json');cfg.write_bytes(encode({'decoded_nodes': bound}))
            p = invoke([sys.executable, '-m', 'ci.newman_publication', 'scan', '--archive', path, '--limits', cfg], label='recognizer-cumulative-scan')
            self.observe('nodes-across-members-' + str(bound), 'scan', p, status, code, data)
        self.finish('accounting', start, len(examples))

    def finish(self, group, start, declared):
        rows = self.rows[start:]
        failed = [dict(id=r['id'], operation=r['operation'], errors=r['errors']) for r in rows if r['errors']]
        summary = dict(group=group, prerequisites='PASS', declared_inputs=declared, actual_attempts=len(rows),
                       operations={op:sum(r['operation'] == op for r in rows) for op in ('scan','project','check')},
                       failed_expectations=len(failed), failures=failed,
                       dependent_check_attempts=sum(r.get('dependent',{}).get('check_attempts',0) for r in rows),
                       dependent_blocked=sum(r.get('dependent',{}).get('status') == 'BLOCKED_BY_PROJECT' for r in rows),
                       actual_reader_after=sum(r.get('dependent',{}).get('reader_after_executed',False) for r in rows))
        # Сам отказ не устанавливает пару. Учитывается фактический исход
        # соответствующего поддержанного control.
        if group == 'recognition':
            scans = [r for r in rows if r['operation'] == 'scan']
            negatives = [r for r in scans if r['side'] == 'negative']
            established = []
            for row in negatives:
                controls = [r for r in scans if r['pair'] == row['pair'] and r['side'] == 'lawful']
                row['paired_controls'] = [r['id'] for r in controls]
                row['pair_established'] = bool(controls) and not row['errors'] and all(not r['errors'] for r in controls)
                established.append(row['pair_established'])
            summary.update(negative_pairs=len(negatives), established_negative_pairs=sum(established),
                           unestablished_negative_pairs=len(established) - sum(established),
                           completed_content_decisions=sum(r['observed'].get('status') in ('CLEAN', 'FINDING') for r in scans),
                           refused_walks=sum(r['observed'].get('status') == 'NOT_EXECUTED' for r in scans))
        (EVIDENCE / ('recognizer-' + group + '-results.json')).write_bytes(encode(rows))
        (EVIDENCE / ('recognizer-' + group + '-summary.json')).write_text(json.dumps(summary, indent=2) + '\n')
        print(json.dumps({k:v for k,v in summary.items() if k != 'failures'}), flush=True)
        self.assertEqual(len(failed), 0, 'Recognition contract mismatch; exact classified captures in recognizer-' + group + '-results.json')


if __name__ == '__main__':
    print('evidence=' + str(EVIDENCE), flush=True)
    unittest.main(verbosity=2)
