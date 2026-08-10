<?php

declare(strict_types=1);

namespace Filekeeper;

class FilekeeperException extends \RuntimeException
{
    public function __construct(
        string $message,
        public readonly int $status = 0,
    ) {
        parent::__construct($message);
    }
}

final class NotFoundException extends FilekeeperException {}
final class UnauthorizedException extends FilekeeperException {}
final class TooLargeException extends FilekeeperException {}
final class UnsupportedTypeException extends FilekeeperException {}
