"""Read recorded runs only. No agent launches, indexing, or network calls."""
import argparse
import collections
import hashlib
import json
from pathlib import Path


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def events(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def claude_trace(harness, cell):
    path = harness / cell['transcript']
    calls, errors, seen_calls, seen_errors = [], [], set(), set()
    for event in events(path):
        content = event.get('message', {}).get('content', [])
        if not isinstance(content, list):
            continue
        for block in content:
            if block.get('type') == 'tool_use' and block.get('id') not in seen_calls:
                seen_calls.add(block['id'])
                if block.get('name') == 'mcp__prism__prism':
                    calls.append({'id': block['id'], 'arguments': block['input']})
            if block.get('type') == 'tool_result' and block.get('is_error'):
                key = block.get('tool_use_id')
                if key not in seen_errors:
                    seen_errors.add(key)
                    errors.append({'id': key, 'content': block.get('content')})
    return {'path': str(path), 'sha256': digest(path), 'prism_calls': calls, 'tool_errors': errors}


def claude_summary(rows):
    out = {}
    for arm in ('native', 'prism_init'):
        cells = [row['cells'][arm + '/nocache'] for row in rows]
        out[arm] = {
            'cells': len(cells), 'resolved': sum(c['resolved'] is True for c in cells),
            'requests': sum(c['api_calls'] for c in cells),
            'input_tokens': sum(c['tokens'] for c in cells),
            'output_tokens': sum(c['tokens_out'] for c in cells),
            'task_5m_usd': sum(c['repriced']['5m']['usd'] - c['repriced']['5m']['startup_usd'] for c in cells),
            'startup_5m_usd': sum(c['repriced']['5m']['startup_usd'] for c in cells),
        }
    return out


def codex_summary(rows):
    out = {}
    for arm in ('native', 'prism_init'):
        cells = [row['cells'][arm] for row in rows]
        parts = collections.Counter()
        for cell in cells:
            for i, u in enumerate(cell['requests']):
                assert u['input_tokens'] <= 272000, 'Revisit tier accounting'
                parts['output_usd'] += u['output_tokens'] * 10 / 1e6
                if i:
                    parts['cached_input_usd'] += u['cached_input_tokens'] * .1 / 1e6
                    parts['fresh_input_usd'] += (u['input_tokens'] - u['cached_input_tokens']) * 2 / 1e6
        out[arm] = {
            'cells': len(cells), 'resolved': sum(c['resolved'] is True for c in cells),
            'requests': sum(c['api_calls'] for c in cells),
            'tool_calls': sum(c['tool_calls'] for c in cells),
            'input_tokens': sum(c['usage']['input_tokens'] for c in cells),
            'output_tokens': sum(c['usage']['output_tokens'] for c in cells),
            'ongoing_usd': sum(c['observed_api_estimate']['ongoing_usd'] for c in cells),
            'startup_input_usd': sum(c['observed_api_estimate']['startup_input_usd'] for c in cells),
            'ongoing_components_usd': dict(parts),
            'prism_ops': dict(collections.Counter(op for c in cells for op in c['prism_ops'])),
        }
        assert abs(sum(parts.values()) - out[arm]['ongoing_usd']) < 1e-8
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--harness', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    runs = {
        'claude_wide': 'nocache32-2026-10-17',
        'claude_issue': 'nocache-issue42-2026-10-17',
        'codex': 'codex-mixed16-warm-2026-10-17-stopped',
    }
    data, sources = {}, {}
    for name, run in runs.items():
        path = args.harness / 'results' / run / 'results.json'
        data[name] = json.loads(path.read_text())
        sources[name] = {'path': str(path), 'sha256': digest(path), 'meta': data[name].get('_meta')}
    pairs = {k: v for k, v in data['codex'].items()
             if not k.startswith('_') and all(a in v.get('cells', {}) for a in ('native', 'prism_init'))}
    result = {'sources': sources, 'summaries': {}, 'matched_tasks': [], 'claude_tool_errors': [],
              'codex_host_notices': [], 'selected_tool_results': []}
    traces = {}
    for name in ('claude_wide', 'claude_issue'):
        rows = [v for k, v in data[name].items() if not k.startswith('_') and 'cells' in v]
        result['summaries'][name] = claude_summary(rows)
        ops = collections.Counter()
        for task, row in data[name].items():
            if task.startswith('_') or 'cells' not in row:
                continue
            for arm in ('native', 'prism_init'):
                trace = claude_trace(args.harness, row['cells'][arm + '/nocache'])
                traces[name, task, arm] = trace
                for error in trace['tool_errors']:
                    result['claude_tool_errors'].append({'run': name, 'task': task, 'arm': arm, **error})
                ops.update(call['arguments'].get('op', '?') for call in trace['prism_calls'])
        result['summaries'][name]['prism_ops_attempted'] = dict(ops)
    both = [row for row in data['claude_issue'].values() if isinstance(row, dict) and 'cells' in row
            and all(row['cells'][a + '/nocache']['resolved'] is True for a in ('native', 'prism_init'))]
    result['summaries']['claude_issue_both_solved_diagnostic'] = claude_summary(both)
    for kind in ('all', 'wide', 'bug'):
        keys = [k for k in pairs if kind == 'all' or k.startswith('wide__') == (kind == 'wide')]
        result['summaries']['codex_' + kind] = codex_summary([pairs[k] for k in keys])
        if kind != 'all':
            name = 'claude_wide' if kind == 'wide' else 'claude_issue'
            result['summaries']['claude_matched_' + kind] = claude_summary([data[name][k] for k in keys])
    for task, row in pairs.items():
        name = 'claude_wide' if task.startswith('wide__') else 'claude_issue'
        item = {'task': task, 'claude': {}, 'codex': {}}
        for arm in ('native', 'prism_init'):
            cell = data[name][task]['cells'][arm + '/nocache']
            item['claude'][arm] = {'requests': cell['api_calls'], **traces[name, task, arm]}
            cell = row['cells'][arm]
            item['codex'][arm] = {k: cell[k] for k in ('api_calls', 'prism_calls', 'prism_ops', 'tool_calls')}
        result['matched_tasks'].append(item)
    codex_dir = args.harness / 'results' / runs['codex']
    for task, row in data['codex'].items():
        if task.startswith('_') or 'cells' not in row:
            continue
        for arm in row['cells']:
            path = codex_dir / 'cells' / task / arm / 'events.jsonl'
            for event in events(path):
                if event.get('type') != 'item.completed':
                    continue
                item = event.get('item', {})
                if item.get('type') == 'error':
                    result['codex_host_notices'].append({'task': task, 'arm': arm, 'message': item.get('message')})
                if item.get('type') == 'mcp_tool_call' and (item.get('error') or
                        (task == 'wide__chi__Routes_Match' and item.get('arguments', {}).get('op') == 'change_impact')):
                    result['selected_tool_results'].append({'task': task, 'arm': arm, 'path': str(path),
                                                           'sha256': digest(path), 'item': item})
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result['summaries'], indent=2))


if __name__ == '__main__':
    main()
