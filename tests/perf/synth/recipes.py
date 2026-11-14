"""The fake engine's `_kiro/workflow/listRecipes` catalog (engine/recipes.json).

7 recipes at x1, the seven sources the fixture's launch rules name, and 7 per scale step beyond,
each with two inputs and a plan of a sequence of four steps.
"""

import json


def write(path, scale):
    """Writes the catalog for `scale` to `path` and returns how many recipes it holds."""
    out = []
    for i in range(7 * scale):
        steps = [
            {
                'nodeId': f'step-{k}',
                'type': 'step',
                'agentName': 'general',
                'modelId': 'auto' if k % 2 else '',
                'effortLevel': '',
                'steps': [],
                'branches': [],
            }
            for k in range(4)
        ]
        out.append(
            {
                'name': f'recipe-{i:02d}',
                'description': f'Synthetic workflow {i}: four steps over a topic.',
                'validationError': '',
                'source': f'/workspace/.kiro/workflows/recipe-{i:02d}.yaml',
                'inputs': {'topic': 'string', 'brief': 'prompt'},
                'plan': {'nodeId': 'root', 'type': 'sequence', 'steps': steps, 'branches': []},
                'builtIn': False,
            }
        )
    with open(path, 'w') as f:
        json.dump(out, f, indent=1)
    return len(out)
