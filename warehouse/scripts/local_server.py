"""Run a task-owned native ClickHouse server; no global configuration changes."""
import argparse
import socket
import subprocess
import time
import urllib.request
from pathlib import Path
from xml.sax.saxutils import escape


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def start(binary, root):
    root = Path(root).resolve()
    root.mkdir(parents=True, exist_ok=True)
    http, tcp = free_port(), free_port()
    config = root / 'config.xml'
    base = escape(str(root))
    config.write_text(f'''<clickhouse>
<logger><level>warning</level><log>{base}/server.log</log><errorlog>{base}/error.log</errorlog><console>false</console></logger>
<path>{base}/data/</path><tmp_path>{base}/tmp/</tmp_path><user_files_path>{base}/user_files/</user_files_path>
<format_schema_path>{base}/format_schemas/</format_schema_path>
<listen_host>127.0.0.1</listen_host><http_port>{http}</http_port><tcp_port>{tcp}</tcp_port>
<max_connections>64</max_connections><max_thread_pool_size>512</max_thread_pool_size>
<background_pool_size>4</background_pool_size><background_schedule_pool_size>4</background_schedule_pool_size>
<background_message_broker_schedule_pool_size>2</background_message_broker_schedule_pool_size>
<background_fetches_pool_size>2</background_fetches_pool_size>
<background_move_pool_size>2</background_move_pool_size>
<merge_tree><number_of_free_entries_in_pool_to_execute_mutation>2</number_of_free_entries_in_pool_to_execute_mutation><number_of_free_entries_in_pool_to_lower_max_size_of_merge>2</number_of_free_entries_in_pool_to_lower_max_size_of_merge><number_of_free_entries_in_pool_to_execute_optimize_entire_partition>2</number_of_free_entries_in_pool_to_execute_optimize_entire_partition></merge_tree>
<profiles><default><max_memory_usage>2000000000</max_memory_usage><max_threads>2</max_threads><join_use_nulls>1</join_use_nulls></default></profiles>
<users><default><password></password><networks><ip>127.0.0.1</ip></networks><profile>default</profile><quota>default</quota><access_management>1</access_management></default></users>
<quotas><default><interval><duration>3600</duration><queries>0</queries><errors>0</errors><result_rows>0</result_rows><read_rows>0</read_rows><execution_time>0</execution_time></interval></default></quotas>
</clickhouse>''')
    log = (root / 'process.log').open('wb')
    proc = subprocess.Popen([str(Path(binary).resolve()), 'server', '--config-file', str(config)], stdout=log, stderr=subprocess.STDOUT)
    log.close()
    endpoint = f'http://127.0.0.1:{http}'
    try:
        for _ in range(150):
            if proc.poll() is not None:
                raise RuntimeError(f'ClickHouse exited {proc.returncode}; inspect {root}/process.log')
            try:
                if urllib.request.urlopen(endpoint + '/ping', timeout=1).read() == b'Ok.\n':
                    return proc, endpoint
            except (OSError, TimeoutError):
                pass
            time.sleep(0.2)
        raise RuntimeError('ClickHouse startup timed out')
    except BaseException:
        proc.terminate()
        proc.wait(timeout=15)
        raise


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--root', required=True)
    args = parser.parse_args()
    process, url = start(args.binary, args.root)
    print(url, flush=True)
    try:
        process.wait()
    except KeyboardInterrupt:
        process.terminate()
        process.wait(timeout=15)
