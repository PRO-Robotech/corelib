"""Синтетические пары по root agreement 5717201675; без импорта SUT.

Все значения вымышлены. Корпус объявляет контекст, исход и одну изменяемую
семантическую величину пары; он не распознаёт и не очищает произвольный input.
"""
import base64
import json
from urllib.parse import quote

SECRET = 'synthetic-recognizer-7189!'


def b64(value):
    return base64.b64encode(value.encode()).decode()


def noncanonical(token):
    """Меняется только незначащий pad bit; discovery bytes остаются прежними."""
    alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
    assert token.endswith('=')
    index = len(token.rstrip('=')) - 1
    result = token[:index] + alphabet[alphabet.index(token[index]) + 1] + token[index + 1:]
    assert result != token and base64.b64decode(result, validate=True) == base64.b64decode(token, validate=True)
    return result


def cases():
    rows = []

    def add(name, payload, status='CLEAN', code='COMPLETE', *, pair=None,
            side='lawful', context='log', partial=False, scene='green'):
        rows.append(dict(id=name, payload=payload, status=status, code=code,
                         pair=pair or name, side=side, context=context,
                         partial_finding=partial, scene=scene))

    diagnostics = (
        ('progress', 'INFO step 1/4 starting build\n', 'INFO [1/4] starting build\n'),
        ('javascript', 'const values = [1, 2];\n', 'const values = [1, 2,];\n'),
        ('basic-prose', 'Standard policy check complete\n', 'Basic policy check complete\n'),
        ('quote-text', 'message ' + b64('hello') + '\n', 'message ' + b64('"hello') + '\n'),
    )
    for name, control, clarified in diagnostics:
        add('diagnostic-' + name + '-control', control, pair=name)
        add('diagnostic-' + name + '-clarified', clarified, pair=name, side='clarified-lawful')

    # В protected-паре меняется только имя чувствительного поля.
    renderers = {
        'ordinary-json': (lambda key: {key: SECRET}, 'structured'),
        'trailing-js-object': (lambda key: 'const a = [' + json.dumps({key: SECRET}) + ',];', 'log'),
        'trailing-js-escaped-key': (lambda key: 'const a = [{"' + ''.join('\\u%04x' % ord(c) for c in key) + '":"' + SECRET + '"},];', 'log'),
        'trailing-js-encoded': (lambda key: 'const a = [' + b64(json.dumps({key: SECRET})) + ',];', 'log'),
        'progress-remainder': (lambda key: 'INFO [1/4] ' + key + '="' + SECRET + '"', 'log'),
        'string-remainder-assignment': (lambda key: '"ready" ' + key + '="' + SECRET + '"', 'log'),
        'string-remainder-object': (lambda key: '"ready" ' + json.dumps({key: SECRET}), 'log'),
        'string-remainder-encoded': (lambda key: '"ready" ' + b64(json.dumps({key: SECRET})), 'log'),
        'decoded-quote-remainder': (lambda key: {'encoding': 'base64', 'data': b64('"hello ' + key + '=' + SECRET)}, 'structured'),
        'decoded-url-quote-remainder': (lambda key: {'encoding': 'url', 'data': quote('"hello ' + key + '=' + SECRET, safe='')}, 'structured'),
        'decoded-buffer-quote-remainder': (lambda key: {'type': 'Buffer', 'data': list(('"hello ' + key + '=' + SECRET).encode())}, 'structured'),
    }
    for name, (render, context) in renderers.items():
        add(name + '-lawful', render('display_name'), pair=name, context=context)
        add(name + '-protected', render('access_token'), 'FINDING', 'SECRET_MATERIAL', pair=name, side='negative', context=context)
    for scene in ('finding', 'precondition', 'script-error'):
        add('outcome-' + scene + '-lawful', 'INFO [1/4] display_name=' + SECRET, pair=scene, scene=scene)
        add('outcome-' + scene + '-protected', 'INFO [1/4] access_token=' + SECRET, 'FINDING', 'SECRET_MATERIAL', pair=scene, side='negative', scene=scene)

    credential = 'u:' + SECRET
    while len(credential.encode()) % 3 == 0:
        credential += '!'
    token = b64(credential)
    for name, text, status, code in [
        ('prose-empty-password', 'note Basic ' + b64('u:') + ' end', 'CLEAN', 'COMPLETE'),
        ('prose-password', 'note Basic ' + token + ' end', 'FINDING', 'SECRET_MATERIAL'),
        ('prose-noncanonical', 'note Basic ' + noncanonical(token) + ' end', 'NOT_EXECUTED', 'MALFORMED_INPUT'),
        ('prose-token-junk', 'note Basic ' + token + '! end', 'NOT_EXECUTED', 'MALFORMED_INPUT'),
        ('string-remainder-url-lawful', '"ready" https://u:@example.invalid/p', 'CLEAN', 'COMPLETE'),
        ('string-remainder-url-protected', '"ready" https://u:' + SECRET + '@example.invalid/p', 'FINDING', 'SECRET_MATERIAL'),
    ]:
        add(name, text, status, code, pair='basic-prose-credential' if name.startswith('prose') else 'remainder-url', side='lawful' if status == 'CLEAN' else 'negative')

    sources = {
        'scalar': lambda text: {'ProXY_authorization': text},
        'postman-key': lambda text: {'key': 'Authorization', 'value': text},
        'postman-name': lambda text: {'name': 'Proxy-Authorization', 'value': text},
        'typed-basic': lambda text: {'type': 'basic', 'basic': [{'key': 'note', 'value': text}]},
        'raw-header': lambda text: 'Authorization \t:\t' + text,
    }
    for source, render in sources.items():
        context = 'log' if source == 'raw-header' else 'structured'
        for tag, text, status, code in [
            ('canonical', 'bAsIc \t' + token + ' \t', 'FINDING', 'SECRET_MATERIAL'),
            ('empty-password', 'Basic ' + b64('u:'),
             'CLEAN' if source == 'typed-basic' else 'FINDING',
             'COMPLETE' if source == 'typed-basic' else 'SECRET_MATERIAL'),
            ('malformed-token', 'Basic %%%', 'NOT_EXECUTED', 'MALFORMED_INPUT'),
            ('valid-prefix-junk', 'Basic ' + token + '!', 'NOT_EXECUTED', 'MALFORMED_INPUT'),
            ('missing-colon', 'Basic ' + b64('hello'), 'NOT_EXECUTED', 'MALFORMED_INPUT'),
            ('noncanonical', 'Basic ' + noncanonical(token), 'NOT_EXECUTED', 'MALFORMED_INPUT'),
            ('non-utf8', 'Basic ' + base64.b64encode(b'u:\xff').decode(), 'NOT_EXECUTED', 'UNSUPPORTED_INPUT'),
        ]:
            add('basic-' + source + '-' + tag, render(text), status, code, pair='basic-' + source,
                side='lawful' if tag in ('canonical', 'empty-password') else 'negative', context=context,
                partial=(source != 'typed-basic' and status == 'NOT_EXECUTED'))
        if source != 'typed-basic':
            for tag, value in [('missing-token', 'Basic'), ('empty-token', 'Basic \t'),
                               ('extra-token', 'Basic ' + token + ' extra')]:
                add('basic-' + source + '-' + tag, render(value), 'NOT_EXECUTED', 'MALFORMED_INPUT',
                    pair='basic-' + source, side='negative', context=context, partial=True)
        for absent in (None, ''):
            if source == 'raw-header' and absent is None:
                continue
            payload = ({'type': 'basic', 'basic': [{'key': 'password', 'value': absent}]}
                       if source == 'typed-basic' else render(absent))
            add('basic-' + source + '-absence-' + ('null' if absent is None else 'empty'), payload,
                pair='absence-' + source, context=context)
        if source == 'raw-header':
            sibling = 'Authorization:\nBasic policy check complete'
        else:
            sibling = render(None if source != 'typed-basic' else '')
            sibling['ordinary_sibling'] = 'Basic policy check complete'
        add('basic-' + source + '-sibling-isolation', sibling, pair='sibling-' + source, context=context)

    add('postman-key-precedes-name', {'key': 'note', 'name': 'Authorization', 'value': 'Basic policy check complete'}, context='structured')
    add('raw-header-token-boundary', 'XAuthorization: Basic policy check complete')
    add('raw-header-next-line-credential', 'Authorization:\nBasic ' + token,
        'FINDING', 'SECRET_MATERIAL', pair='sibling-raw-header', side='negative')
    add('raw-header-next-line-assignment', 'Authorization:\naccess_token=' + SECRET,
        'FINDING', 'SECRET_MATERIAL', pair='sibling-raw-header', side='negative')
    add('typed-basic-password-entry', {'type': 'basic', 'basic': [{'key': 'password', 'value': SECRET}]},
        'FINDING', 'SECRET_MATERIAL', pair='absence-typed-basic', side='negative', context='structured')
    # Раскрытый родитель заново устанавливает структурный контекст детей.
    for name, text, status, code in [('canonical', 'Basic ' + token, 'FINDING', 'SECRET_MATERIAL'),
                                    ('malformed', 'Basic %%%', 'NOT_EXECUTED', 'MALFORMED_INPUT')]:
        payload = {'encoding': 'base64', 'data': b64(json.dumps({'Authorization': text}))}
        add('decoded-basic-parent-' + name, payload, status, code, pair='decoded-basic-parent',
            side='lawful' if name == 'canonical' else 'negative', context='structured', partial=name == 'malformed')

    # После поиска span строгий decoder обязан получить исходные байты.
    strict = [
        ('plain-duplicate', '{"access_token":null,"access_token":null}'),
        ('escaped-duplicate', '{"\\u0061ccess_token":"' + SECRET + '","\\u0061ccess_token":null}'),
        ('nan', '{"ordinary":NaN}'),
        ('introduced-object', '{ \t\r\n"ordinary":'),
        ('introduced-escaped-key', '{"\\u0061ccess_token":'),
    ]
    for name, malformed in strict:
        for form, render in [('whole', lambda text: text), ('remainder', lambda text: '"ready" ' + text),
                             ('js', lambda text: 'const a = [' + text + ',];'),
                             ('decoded', lambda text: b64(text))]:
            pair = 'strict-' + name + '-' + form
            add(pair + '-lawful', render('{"ordinary":null}'), pair=pair)
            add(pair + '-negative', render(malformed), 'NOT_EXECUTED', 'MALFORMED_INPUT', pair=pair, side='negative')
    for name, value, code in [
        ('unknown-explicit', {'encoding': 'rot13', 'data': b64('hello')}, 'UNSUPPORTED_ENCODING'),
        ('malformed-explicit', {'encoding': 'base64', 'data': '%%%!' }, 'MALFORMED_INPUT'),
        ('malformed-buffer', {'type': 'Buffer', 'data': [256]}, 'MALFORMED_INPUT'),
    ]:
        add(name + '-lawful', {'encoding': 'base64', 'data': b64('hello')}, pair=name, context='structured')
        add(name + '-negative', value, 'NOT_EXECUTED', code, pair=name, side='negative', context='structured')
    encoded_secret = json.dumps({'access_token': SECRET}, separators=(',', ':'))
    while len(encoded_secret.encode()) % 3 == 0:
        encoded_secret += ' '
    canonical = b64(encoded_secret)
    add('encoded-json-canonical', canonical, 'FINDING', 'SECRET_MATERIAL', pair='encoded-json-canonical')
    add('encoded-json-pad-bits', noncanonical(canonical), 'NOT_EXECUTED', 'MALFORMED_INPUT',
        pair='encoded-json-canonical', side='negative')
    assert len({r['id'] for r in rows}) == len(rows)
    return rows


def accounting_cases():
    # (Типизированный .json input, число узлов representations, максимальный unwrap depth).
    # Dict/list/scalar считаются; ключи и базовые pathname — нет.
    # У envelope три базовых узла. Buffer включает каждый byte scalar.
    plain = '"hello'
    bracket = '["x",]'
    examples = [
        ('quote-ordinary', plain, 1, 0),
        ('array-ordinary', '[1, 2,]', 1, 0),
        ('array-inner-string', bracket, 2, 1),
        ('two-json-spans', '"x" {"ok":true}', 4, 1),
    ]
    for name, text, decoded_nodes, depth in [('quote', plain, 1, 0), ('inner-string', bracket, 2, 1)]:
        for encoding in ('base64', 'base64url', 'url'):
            data = quote(text, safe='') if encoding == 'url' else b64(text)
            examples.append((encoding + '-' + name, {'encoding': encoding, 'data': data}, 3 + decoded_nodes, 1 + depth))
        examples.append(('buffer-' + name, {'type': 'Buffer', 'data': list(text.encode())}, 3 + len(text.encode()) + decoded_nodes, 1 + depth))
    leaf = plain
    for _ in range(8):
        leaf = json.dumps(leaf)
    examples.append(('default-depth-eight', leaf, 9, 8))
    examples.append(('default-depth-nine', json.dumps(leaf), 10, 9))
    return examples
