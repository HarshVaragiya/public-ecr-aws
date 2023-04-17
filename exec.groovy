pipeline {
    agent any
    stages {
        stage("build"){
            steps {
                sh 'docker build -t public-ecr-aws:latest .'
            }
        }
        stage("exec") {
            steps{
                sh 'docker run --rm public-ecr-aws:latest ${ARGS}'
            }
        }
        stage("cleanup"){
            steps {
                sh 'docker image rm public-ecr-aws:latest'
                echo 'cleaning up'
                sh 'rm -rf * && rm -rf .git*'
                sh 'ls -alh'
            }
        }
    }
}