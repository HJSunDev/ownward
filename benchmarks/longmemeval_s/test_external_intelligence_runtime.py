from pathlib import Path
import tempfile, unittest
from unittest import mock
import external_intelligence_runtime as subject
from external_intelligence import ExternalIntelligenceExecutor, InvocationLifecycle
import test_run  # Both benchmark suites have run.py; establish the local test module first.

class RuntimePortTests(unittest.TestCase):
    def test_adapter_is_loaded_from_explicit_external_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'client.py'
            path.write_text('DRIVER = "fixture/v1"\n',encoding='utf-8')
            with mock.patch.object(subject,'_ADAPTERS',{'fixture/v1':str(path)}), mock.patch.object(subject,'select_runtime_implementation'):
                self.assertEqual('fixture/v1',subject._adapter('fixture/v1').DRIVER)

    def test_missing_external_adapter_fails_before_execution(self):
        with mock.patch.object(subject,'_ADAPTERS',{'fixture/v1':'missing/adapter.py'}), mock.patch.object(subject,'select_runtime_implementation'):
            with self.assertRaisesRegex(subject.ExternalIntelligenceError,'does not exist'):
                subject._adapter('fixture/v1')

    def test_provider_neutral_partial_output_survives_runtime_port(self):
        error = subject.ExternalIntelligenceError('incomplete structured batch')
        error.partial_output = {'analyses': [{'index': 0}, None]}
        error.partial_usage = {'output_tokens': 12}
        inner = mock.Mock()
        inner.invoke.side_effect = error
        adapter = mock.Mock(TransportError=subject.ExternalIntelligenceError,
                            TransportTimeout=subject.ExternalIntelligenceTimeout)
        port = subject._StableTransport(inner, adapter)
        with self.assertRaises(subject.ExternalIntelligenceError) as caught:
            port.invoke(prompt='test')
        self.assertIs(error, caught.exception)
        self.assertEqual({'analyses': [{'index': 0}, None]}, caught.exception.partial_output)


    def test_adapter_timeout_progress_reaches_the_shared_retry_loop(self):
        for timeout_type in (TimeoutError, subject.ExternalIntelligenceTimeout):
            with self.subTest(timeout_type=timeout_type), tempfile.TemporaryDirectory() as directory:
                transport = mock.Mock()
                transport.identity = subject.RuntimeIdentity(
                    driver='test/v1', provider='test', transport='in-process-test/v1',
                    selection_sha256='d'*64, artifact_sha256='a'*64, implementation_sha256='e'*64,
                    credential_locator_sha256='b'*64, max_active=1, worker_processes=1).value()
                transport.new_scope.return_value = transport
                transport.diagnostics.return_value = {'rate_limit_observed': False}
                error = timeout_type('interrupted')
                error.working_notes = 'preserved work'
                error.partial_output = {'analyses': [None]}
                transport.invoke.side_effect = [error, ({'answer': 'done'}, {}, {})]
                adapter = mock.Mock(TransportTimeout=timeout_type, TransportError=RuntimeError)
                stable = subject._StableTransport(transport, adapter).new_scope()
                value, usage = ExternalIntelligenceExecutor(stable).invoke(
                    role='reader', prompt='original', schema={'type': 'object'}, stage=Path(directory),
                    model='model', effort='xhigh', timeout_seconds=240, attempts=2,
                    lifecycle=InvocationLifecycle(retrieval_mode='no-tools',
                        resume_prompt=lambda notes: 'original\n' + notes))
                self.assertEqual({'answer': 'done'}, value)
                self.assertEqual(['original', 'original\npreserved work'],
                                 [call.kwargs['prompt'] for call in transport.invoke.call_args_list])
                self.assertEqual(2, usage['attempts'])
                self.assertTrue((Path(directory)/'attempt-001/interrupted-work.json').is_file())


    def test_provider_neutral_timeout_preserves_structured_progress(self):
        error = subject.ExternalIntelligenceTimeout('interrupted correction')
        error.partial_output = {'analyses': [{'index': 0}, None]}
        inner = mock.Mock()
        inner.invoke.side_effect = error
        adapter = mock.Mock(TransportTimeout=subject.ExternalIntelligenceTimeout,
                            TransportError=subject.ExternalIntelligenceError)
        with self.assertRaises(subject.ExternalIntelligenceTimeout) as caught:
            subject._StableTransport(inner, adapter).invoke(prompt='test')
        self.assertIs(error, caught.exception)
