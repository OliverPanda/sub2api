import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
PLAN = ROOT / '.github/release-tools/plan-auto-release.sh'


class AutoReleasePlanTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name) / 'repo'
        self.origin = Path(self.temp.name) / 'origin.git'
        self.repo.mkdir()
        self.git('init', '--initial-branch=main')
        self.git('config', 'user.name', 'Release Test')
        self.git('config', 'user.email', 'release@example.test')
        subprocess.run(['git', 'init', '--bare', '--initial-branch=main', str(self.origin)], check=True, cwd=self.repo)
        self.git('remote', 'add', 'origin', str(self.origin))
        self.write('backend/app.txt', 'one\n')
        self.commit('initial application')
        self.git('tag', '-a', 'v0.2.21', '-m', 'release v0.2.21')
        self.git('push', '--atomic', 'origin', 'main', 'refs/tags/v0.2.21')

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.repo, check=True, text=True, capture_output=True).stdout.strip()

    def write(self, path, content):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def commit(self, message):
        self.git('add', '.')
        self.git('commit', '-m', message)

    def plan(self, tested_sha=None, event='workflow_run'):
        tested_sha = tested_sha or self.git('rev-parse', 'HEAD')
        output = Path(self.temp.name) / 'github-output'
        output.write_text('')
        env = {
            **os.environ,
            'EVENT_NAME': event,
            'TESTED_SHA': tested_sha,
            'DEFAULT_BRANCH': 'main',
            'GITHUB_OUTPUT': str(output),
        }
        subprocess.run(['bash', str(PLAN)], cwd=self.repo, env=env, check=True, capture_output=True, text=True)
        values = {}
        for line in output.read_text().splitlines():
            if '=' in line:
                key, value = line.split('=', 1)
                values[key] = value
        return values

    def test_application_change_plans_next_patch_and_atomic_push(self):
        self.write('backend/app.txt', 'two\n')
        self.commit('application change')
        self.git('push', 'origin', 'main')

        planned = self.plan()
        self.assertEqual(planned['release'], 'true')
        self.assertEqual(planned['previous_tag'], 'v0.2.21')
        self.assertEqual(planned['version'], '0.2.22')
        self.assertEqual(planned['tag'], 'v0.2.22')

        self.write('backend/cmd/server/VERSION', '0.2.22\n')
        self.commit('chore(release): v0.2.22')
        self.git('tag', '-a', 'v0.2.22', '-m', 'release v0.2.22')
        self.git('push', '--atomic', 'origin', 'main', 'refs/tags/v0.2.22')
        self.assertEqual(self.git('rev-parse', 'HEAD'), self.git('--git-dir', str(self.origin), 'rev-parse', 'main'))
        self.assertEqual(self.git('rev-parse', 'v0.2.22^{commit}'), self.git('--git-dir', str(self.origin), 'rev-parse', 'v0.2.22^{commit}'))

    def test_docs_only_change_is_skipped(self):
        self.write('docs/notes.md', 'documentation\n')
        self.commit('docs only')
        self.git('push', 'origin', 'main')
        self.assertEqual(self.plan()['release'], 'false')

    def test_already_tagged_head_is_skipped_without_loop(self):
        self.assertEqual(self.plan()['release'], 'false')

    def test_stale_ci_result_is_skipped(self):
        self.write('backend/app.txt', 'two\n')
        self.commit('application change')
        self.git('push', 'origin', 'main')
        stale_sha = self.git('rev-parse', 'HEAD~1')
        self.assertEqual(self.plan(tested_sha=stale_sha)['release'], 'false')

    def test_workflow_uses_ephemeral_push_credentials(self):
        workflow = (ROOT / '.github/workflows/auto-release.yml').read_text()
        self.assertIn('GIT_ASKPASS="$askpass"', workflow)
        self.assertIn('git push --atomic', workflow)
        self.assertIn('RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}', workflow)
        self.assertNotIn('https://x-access-token:', workflow)


if __name__ == '__main__':
    unittest.main()
