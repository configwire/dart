import 'dart:io';

import 'package:config_nest/config_nest.dart';

/// Defaults demo (cold path) + live-fetch demo (used by T12 live smoke).
///
/// Live mode (env vars; apiKey printed NEVER — values only):
///   CN_BASE_URL=http://127.0.0.1:8102 CN_API_KEY=`<sdk-key>` CN_ENV=dev \
///     CN_CACHE=/tmp/cn-t12-smoke/cache.json dart run example/main.dart
Future<void> main() async {
  final baseUrl = Platform.environment['CN_BASE_URL'] ?? 'http://localhost:8090';
  final apiKey = Platform.environment['CN_API_KEY'] ?? 'demo-key';
  final env = Platform.environment['CN_ENV'] ?? 'dev';
  final cache = Platform.environment['CN_CACHE'];
  final live = Platform.environment['CN_LIVE'] == '1';

  final cn = ConfigNest(
    apiKey: apiKey,
    env: env,
    baseUrl: baseUrl,
    defaults: {'welcome': 'hello', 'enabled': true, 'launch_flag': false},
    cacheFile: cache,
    // Dev smoke: no throttle so repeated runs always hit the server.
    minimumFetchInterval: Duration.zero,
  );

  // ignore: avoid_print
  print('welcome=${cn.getString('welcome')} enabled=${cn.getBool('enabled')}');

  if (live) {
    await cn.ensureInitialized();
    // NOTE: values printed, apiKey never printed.
    // ignore: avoid_print
    print(
      'live_fetch status=${cn.lastFetchStatus} version=${cn.version} '
      'etag=${cn.etag} launch_flag=${cn.getBool('launch_flag')} '
      'welcome=${cn.getString('welcome')} variants=${cn.getVariants()}',
    );
  }
  await cn.dispose();
}
