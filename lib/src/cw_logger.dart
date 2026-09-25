import 'package:lite_logger/lite_logger.dart';

/// Shared ConfigWire logger.
///
/// The SDK [apiKey] must NEVER be logged. It is only sent as the
/// `X-ConfigWire-Key` header on SDK requests.
final LiteLogger cwLogger = LiteLogger(
  name: 'ConfigWire',
  minLevel: LogLevel.debug,
);

/// Logs [message] at debug level only when [verbose] is true.
///
/// Takes a lazy closure so no string is built when [verbose] is false.
void cwDebug(bool verbose, Object? Function() message) {
  if (!verbose) return;
  cwLogger.debug(message);
}
